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
	caps.MaxResources = plugin.Resources{CPU: 4000, Memory: memory}

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
