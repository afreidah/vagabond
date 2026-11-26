// -------------------------------------------------------------------------------
// Container Hardening
//
// Author: Alex Freidah
//
// What every container runs under besides its limits: no gaining privileges
// through setuid programs, a short list of capabilities, containerd's default
// seccomp profile, and a user namespace of its own, so root inside the
// container is an unprivileged user on the node. Each container's user IDs map
// to a range of the node's that no other container has, through ID-mapped
// mounts, so no image is copied to change who owns its files.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/contrib/seccomp"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/defaults"
	"github.com/containerd/containerd/v2/pkg/cap"
	"github.com/containerd/containerd/v2/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// defaultCaps are the capabilities a container keeps: enough to change file
// owners and modes, switch users, signal its own processes and listen on low
// ports. -cap-add adds to them.
var defaultCaps = []string{
	"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_FSETID",
	"CAP_KILL", "CAP_NET_BIND_SERVICE", "CAP_SETGID", "CAP_SETUID",
}

// Each container's 65536 user and group IDs map to the nth range of the node's
// from firstHostID, far above any real user's. The last range stops short of
// 2^32 - 1, which is no ID at all.
const (
	idsPerRange = 1 << 16
	firstHostID = 1 << 30
	idRanges    = (1<<32-firstHostID)/idsPerRange - 1
)

// remapIDs is the capability a snapshotter advertises when it can mount images
// ID-mapped.
const remapIDs = "remap-ids"

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// capabilities is defaultCaps with add, each name in upper case with its CAP_
// prefix. A name the kernel does not know is an error.
func capabilities(add []string) ([]string, error) {
	caps := slices.Clone(defaultCaps)

	for _, name := range add {
		name = strings.ToUpper(name)
		if !strings.HasPrefix(name, "CAP_") {
			name = "CAP_" + name
		}

		if !slices.Contains(cap.Known(), name) {
			return nil, fmt.Errorf("unknown capability %q", name)
		}

		if !slices.Contains(caps, name) {
			caps = append(caps, name)
		}
	}

	return caps, nil
}

// -------------------------------------------------------------------------
// USER NAMESPACES
// -------------------------------------------------------------------------

// idMapping maps a container's 65536 IDs to the nth range of the node's.
func idMapping(n int) []specs.LinuxIDMapping {
	return []specs.LinuxIDMapping{{ContainerID: 0, HostID: uint32(firstHostID + n*idsPerRange), Size: idsPerRange}} //nolint:gosec // n < idRanges
}

// takeRange reserves the lowest ID range no container holds or is being made
// with, and returns it with a function that ends the reservation, called once
// the container carries the range as a label. It is -1, with nothing
// reserved, when user namespaces are off.
func (c *Containerd) takeRange(ctx context.Context) (int, func(), error) {
	if c.cfg.NoUserNS {
		return -1, func() {
			// Nothing was reserved, so nothing to end
		}, nil
	}

	c.ranges.Lock()
	defer c.ranges.Unlock()

	// Listed under the lock, so a container made since the last reservation counts
	containers, err := c.client.Containers(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("listing containers: %w", err)
	}

	used := make(map[int]bool, len(containers)+len(c.taking))
	for n := range c.taking {
		used[n] = true
	}

	for _, container := range containers {
		labels, err := labelsOf(ctx, container)
		if err != nil {
			return 0, nil, err
		}

		used[labels.idRange] = true
	}

	for n := range idRanges {
		if !used[n] {
			c.taking[n] = true

			return n, func() {
				c.ranges.Lock()
				delete(c.taking, n)
				c.ranges.Unlock()
			}, nil
		}
	}

	return 0, nil, fmt.Errorf("every one of the node's %d user ID ranges is taken", idRanges)
}

// idMapped fails unless the snapshotter containers are made with can mount
// images ID-mapped, which user namespaces need: overlayfs with containerd 2.0
// or newer on Linux 5.19 or newer.
func (c *Containerd) idMapped(ctx context.Context) error {
	name := defaults.DefaultSnapshotter
	if labels, err := c.client.NamespaceService().Labels(ctx, c.cfg.Namespace); err == nil && labels[defaults.DefaultSnapshotterNSLabel] != "" {
		name = labels[defaults.DefaultSnapshotterNSLabel]
	}

	caps, err := c.client.GetSnapshotterCapabilities(ctx, name)
	if err != nil {
		return fmt.Errorf("reading what containerd's %s snapshotter can do: %w", name, err)
	}

	if !slices.Contains(caps, remapIDs) {
		return fmt.Errorf("containerd's %s snapshotter cannot mount images ID-mapped, which user namespaces need: "+
			"run containerd 2.0 or newer on Linux 5.19 or newer, or start the agent with -no-userns", name)
	}

	return nil
}

// -------------------------------------------------------------------------
// SPEC
// -------------------------------------------------------------------------

// hardening is what a container runs under besides its limits: no new
// privileges, its capabilities, the default seccomp profile, which reads the
// capabilities so follows them, the user namespace of ID range n unless n is
// -1, and with -read-only-root, a read-only root with /tmp writable.
func (c *Containerd) hardening(n int) []oci.SpecOpts {
	opts := []oci.SpecOpts{oci.WithNoNewPrivileges, oci.WithCapabilities(c.caps), seccomp.WithDefaultProfile()}

	if n >= 0 {
		opts = append(opts, oci.WithUserNamespace(idMapping(n), idMapping(n)))
	}

	if c.cfg.ReadOnlyRoot {
		opts = append(opts, oci.WithRootFSReadonly(), withWritableTmp)
	}

	return opts
}

// snapshotFor is how a container's snapshot is made: ID-mapped to range n,
// unless n is -1.
func snapshotFor(n int) []snapshots.Opt {
	if n < 0 {
		return nil
	}

	host := idMapping(n)[0].HostID

	return []snapshots.Opt{client.WithRemapperLabels(0, host, 0, host, idsPerRange)}
}

// withNodeSys bind-mounts the node's /sys, read-only, in place of the sysfs a
// container mounts itself. A user namespace can mount sysfs only in a network
// namespace it owns, so a container in one on the node's network needs this.
func withNodeSys(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
	for i, m := range s.Mounts {
		if m.Destination == "/sys" {
			s.Mounts[i] = specs.Mount{
				Destination: "/sys", Type: "bind", Source: "/sys", Options: []string{"rbind", "nosuid", "noexec", "nodev", "ro"},
			}
		}
	}

	return nil
}

// withWritableTmp mounts an empty in-memory /tmp, the one place a workload on
// a read-only root can write. It counts against the workload's memory.
func withWritableTmp(_ context.Context, _ oci.Client, _ *containers.Container, s *oci.Spec) error {
	s.Mounts = append(s.Mounts, specs.Mount{
		Destination: "/tmp", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=1777"},
	})

	return nil
}
