// -------------------------------------------------------------------------------
// Container Hardening Tests
//
// Author: Alex Freidah
//
// The parts of hardening that need no containerd: the capability list and the
// names -cap-add takes, the ID ranges and the labels a snapshot is made with,
// and what the spec options set. Real workloads are in the containerd
// executor tests.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// Added capabilities join the defaults once each, in any case and with or
// without their CAP_ prefix; one the kernel does not know is refused.
func TestCapabilities(t *testing.T) {
	t.Parallel()

	caps, err := capabilities([]string{"net_raw", "CAP_CHOWN", "CAP_NET_RAW"})
	if err != nil || !slices.Equal(caps, append(slices.Clone(defaultCaps), "CAP_NET_RAW")) {
		t.Errorf("capabilities() = %v, %v; want the defaults and CAP_NET_RAW once", caps, err)
	}

	if _, err := capabilities([]string{"CAP_FLY"}); err == nil || !strings.Contains(err.Error(), "CAP_FLY") {
		t.Errorf("capabilities(CAP_FLY) = %v, want it refused by name", err)
	}
}

// New refuses a capability the kernel does not know before reaching
// containerd, so the agent stops at startup rather than at a workload.
func TestNew_RefusesAnUnknownCapability(t *testing.T) {
	t.Parallel()

	if _, err := New(&Config{CapAdd: []string{"fly"}}); err == nil || !strings.Contains(err.Error(), `unknown capability "CAP_FLY"`) {
		t.Errorf("New() = %v, want the capability refused", err)
	}
}

// labelled is a container with labels, and no other method set.
type labelled struct {
	client.Container

	labels map[string]string
}

// ID names the container.
func (labelled) ID() string { return "exec-1" }

// Labels returns the labels it was given.
func (l labelled) Labels(context.Context) (map[string]string, error) { return l.labels, nil }

// A container without a range label, as one made with -no-userns or before
// ranges, holds no range.
func TestLabelsOf_NoIDRange(t *testing.T) {
	t.Parallel()

	got, err := labelsOf(t.Context(), labelled{labels: map[string]string{}})
	if err != nil || got.idRange != -1 {
		t.Errorf("labelsOf() range = %d, %v; want -1", got.idRange, err)
	}
}

// Each range maps the container's 65536 IDs to its own run of the node's, and
// a snapshot made for it carries the same mapping as labels.
func TestIDRanges(t *testing.T) {
	t.Parallel()

	if got := idMapping(2)[0]; got.ContainerID != 0 || got.HostID != firstHostID+2*idsPerRange || got.Size != idsPerRange {
		t.Errorf("idMapping(2) = %+v, want 65536 IDs from %d", got, firstHostID+2*idsPerRange)
	}

	if snapshotFor(-1) != nil {
		t.Error("snapshotFor(-1) maps IDs, want nothing")
	}

	var info snapshots.Info
	for _, opt := range snapshotFor(1) {
		if err := opt(&info); err != nil {
			t.Fatal(err)
		}
	}

	if want := "0:1073807360:65536"; info.Labels[snapshots.LabelSnapshotUIDMapping] != want || info.Labels[snapshots.LabelSnapshotGIDMapping] != want {
		t.Errorf("snapshot labels = %v, want both mappings %s", info.Labels, want)
	}
}

// The spec gets no new privileges, the capabilities, the default seccomp
// profile, the user namespace of its range, and with -read-only-root a
// read-only root and a writable /tmp.
func TestHardening(t *testing.T) {
	t.Parallel()

	c := &Containerd{caps: defaultCaps, cfg: Config{ReadOnlyRoot: true}}

	s := &oci.Spec{Process: &specs.Process{}, Root: &specs.Root{}, Linux: &specs.Linux{}}
	for _, opt := range c.hardening(3) {
		if err := opt(t.Context(), nil, nil, s); err != nil {
			t.Fatal(err)
		}
	}

	switch {
	case !s.Process.NoNewPrivileges:
		t.Error("no_new_privileges not set")
	case !slices.Equal(s.Process.Capabilities.Bounding, defaultCaps):
		t.Errorf("capabilities = %v, want the defaults", s.Process.Capabilities.Bounding)
	case s.Linux.Seccomp == nil:
		t.Error("no seccomp profile")
	case len(s.Linux.UIDMappings) != 1 || s.Linux.UIDMappings[0] != idMapping(3)[0]:
		t.Errorf("uid mappings = %v, want range 3's", s.Linux.UIDMappings)
	case !s.Root.Readonly || len(s.Mounts) != 1 || s.Mounts[0].Destination != "/tmp":
		t.Errorf("root read-only %t with mounts %v, want read-only with /tmp", s.Root.Readonly, s.Mounts)
	}

	// No range is no user namespace, and the root stays writable without the flag
	c.cfg.ReadOnlyRoot = false
	plain := &oci.Spec{Process: &specs.Process{}, Root: &specs.Root{}, Linux: &specs.Linux{}}

	for _, opt := range c.hardening(-1) {
		if err := opt(t.Context(), nil, nil, plain); err != nil {
			t.Fatal(err)
		}
	}

	if len(plain.Linux.Namespaces) > 0 || plain.Root.Readonly {
		t.Errorf("namespaces %v, root read-only %t; want neither", plain.Linux.Namespaces, plain.Root.Readonly)
	}
}

// The node's /sys replaces the container's own sysfs, read-only, and every
// other mount is left as it was.
func TestWithNodeSys(t *testing.T) {
	t.Parallel()

	s := &oci.Spec{Mounts: []specs.Mount{
		{Destination: "/proc", Type: "proc", Source: "proc"},
		{Destination: "/sys", Type: "sysfs", Source: "sysfs"},
	}}

	if err := withNodeSys(t.Context(), nil, nil, s); err != nil {
		t.Fatal(err)
	}

	if sys := s.Mounts[1]; sys.Type != "bind" || sys.Source != "/sys" || !slices.Contains(sys.Options, "ro") || s.Mounts[0].Type != "proc" {
		t.Errorf("mounts = %+v, want the node's /sys bound read-only and /proc untouched", s.Mounts)
	}
}
