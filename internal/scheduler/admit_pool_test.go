// -------------------------------------------------------------------------------
// Pool Admission Tests
//
// Author: Alex Freidah
//
// A pool is admitted on its members, each judged on its own. The case that
// matters: properties spread across nodes must never add up to one that no
// node has.
// -------------------------------------------------------------------------------

package scheduler

import (
	"slices"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// member is a pool node with the baseline's capabilities, an architecture, the
// memory it has free, and labels.
func member(name string, arch job.Arch, memory int, labels map[string]string) plugin.Member {
	caps := baseInput().Capabilities
	caps.Architectures = []job.Arch{arch}
	caps.MaxResources = plugin.Limits{CPU: new(4000), Memory: new(memory)}

	return plugin.Member{Name: name, Capabilities: caps, Labels: labels}
}

// poolInput is the baseline provider made a pool of members.
func poolInput(members ...plugin.Member) Input {
	in := baseInput()
	in.Provider = "homelab"
	in.Capabilities.Members = members

	return in
}

// armWithMemory asks for arm64 and 8 GiB together.
func armWithMemory() *Request {
	req := baseRequest()
	req.Task.Execution = &job.ExecutionRequirements{Architecture: new(job.ArchARM64)}
	req.Task.Resources = &job.Resources{Memory: new(8192)}

	return req
}

// A node with arm64 and a node with the memory do not make a pool that can run
// a task needing both.
func TestAdmit_PoolPropertiesDoNotCombine(t *testing.T) {
	t.Parallel()

	in := poolInput(
		member("arm-small", job.ArchARM64, 2048, nil),
		member("amd-big", job.ArchAMD64, 16384, nil),
	)

	result := admitOnly(t, armWithMemory(), &in)

	if result.Admitted() {
		t.Fatalf("admitted on nodes that each have half of what the task needs: %+v", result.Candidates)
	}

	if !strings.Contains(result.Rejections[0].Detail, "closest node") {
		t.Errorf("detail = %q, want the closest node named", result.Rejections[0].Detail)
	}
}

// A node that has everything is the only one admitted, and placement is told
// so.
func TestAdmit_PoolAdmitsTheNodesThatFit(t *testing.T) {
	t.Parallel()

	in := poolInput(
		member("arm-small", job.ArchARM64, 2048, nil),
		member("amd-big", job.ArchAMD64, 16384, nil),
		member("arm-big", job.ArchARM64, 16384, nil),
	)

	result := admitOnly(t, armWithMemory(), &in)

	if !result.Admitted() {
		t.Fatalf("rejected: %+v", result.Rejections)
	}

	if got := result.Candidates[0].Members; !slices.Equal(got, []string{"arm-big"}) {
		t.Errorf("members = %v, want only arm-big", got)
	}
}

// A node with no room left admits nothing, so a pool whose nodes are all full
// is rejected on resources, naming the closest node.
func TestAdmit_PoolFullNodesAreRejected(t *testing.T) {
	t.Parallel()

	in := poolInput(
		member("full-a", job.ArchAMD64, 0, nil),
		member("full-b", job.ArchAMD64, 0, nil),
	)

	req := baseRequest()
	req.Task.Resources = &job.Resources{Memory: new(256)}

	result := admitOnly(t, req, &in)

	if result.Admitted() {
		t.Fatalf("admitted onto full nodes: %+v", result.Candidates)
	}

	rejection := result.Rejections[0]
	if rejection.Reason != ReasonResourcesExceeded || !strings.Contains(rejection.Detail, "closest node") {
		t.Errorf("rejection = %s %q, want resources-exceeded naming the closest node", rejection.Reason, rejection.Detail)
	}
}

// A task that declares no size is admitted only where the node's default size
// fits, which is the size it would be placed and run at.
func TestAdmit_PoolUndeclaredSizeUsesTheDefault(t *testing.T) {
	t.Parallel()

	small := member("small", job.ArchAMD64, 512, nil)
	big := member("big", job.ArchAMD64, 4096, nil)

	for _, m := range []*plugin.Member{&small, &big} {
		m.Capabilities.DefaultResources = plugin.Resources{CPU: 1000, Memory: 1024}
	}

	result := admitOnly(t, baseRequest(), new(poolInput(small, big)))

	if !result.Admitted() || !slices.Equal(result.Candidates[0].Members, []string{"big"}) {
		t.Errorf("result = %+v, want only the node with room for the default size", result)
	}
}

// A constraint on a node label narrows the pool to the nodes that carry it.
func TestAdmit_PoolNodeLabels(t *testing.T) {
	t.Parallel()

	in := poolInput(
		member("plain", job.ArchAMD64, 4096, map[string]string{"gpu": "no"}),
		member("gpu", job.ArchAMD64, 4096, map[string]string{"gpu": "yes"}),
	)

	req := baseRequest()
	req.Routing = &job.Routing{Constraints: []job.Constraint{{
		Attribute: plugin.NodeLabelPrefix + "gpu", Operator: job.OperatorEqual, Value: "yes",
	}}}

	result := admitOnly(t, req, &in)

	if !result.Admitted() || !slices.Equal(result.Candidates[0].Members, []string{"gpu"}) {
		t.Errorf("result = %+v, want only the labelled node", result)
	}
}
