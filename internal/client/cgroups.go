// -------------------------------------------------------------------------------
// Cgroup Delegation
//
// Author: Alex Freidah
//
// Workloads are created under the agent's own cgroup, so the limits on it hold
// for everything the agent runs. cgroup v2 lets a group either hold processes
// or hand controllers to children, not both, so the agent first moves itself
// into a leaf of its own group, then enables the cpu and memory controllers
// for the children its workloads will be.
// -------------------------------------------------------------------------------

package client

import (
	"errors"
	"fmt"
	"os"
	"path"

	"github.com/containerd/cgroups/v3/cgroup2"
)

// agentLeaf is the child of the agent's cgroup the agent process moves into.
const agentLeaf = "agent"

// Delegate prepares own, the agent's cgroup, to parent workloads and returns
// the path workloads are created under. An agent restarted inside its own leaf
// uses the leaf's parent.
func Delegate(own string) (string, error) {
	parent := own
	if path.Base(own) == agentLeaf {
		parent = path.Dir(own)
	}

	if parent == "/" {
		return "", errors.New(
			"the agent's cgroup is the root. In a container, run it with --cgroupns=host " +
				"so it sees its real cgroup, or pass -cgroup-parent")
	}

	group, err := cgroup2.Load(parent)
	if err != nil {
		return "", fmt.Errorf("loading cgroup %s: %w", parent, err)
	}

	leaf, err := group.NewChild(agentLeaf, nil)
	if err != nil {
		return "", notDelegated(parent, err)
	}

	if err := leaf.AddProc(uint64(os.Getpid())); err != nil { //nolint:gosec // a pid is positive
		return "", notDelegated(parent, err)
	}

	if err := group.ToggleControllers([]string{"cpu", "memory"}, cgroup2.Enable); err != nil {
		return "", notDelegated(parent, err)
	}

	return parent, nil
}

// notDelegated explains a cgroup the agent may not manage, with the two usual
// fixes.
func notDelegated(group string, err error) error {
	return fmt.Errorf(
		"cannot manage cgroups under %s: %w. Under systemd, set Delegate=yes on the agent's unit; "+
			"in a container, mount /sys/fs/cgroup writable with --cgroupns=host", group, err)
}
