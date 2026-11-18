// -------------------------------------------------------------------------------
// Wire Conversions Tests
//
// Author: Alex Freidah
//
// A workload's charge on a node, a node's executor found by name, and a status
// whose state internal/execution does not name refused on the way in.
// -------------------------------------------------------------------------------

package agentrpc

import (
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// A workload takes what it declared, plus its executor's overhead when it has
// one.
func TestCharge(t *testing.T) {
	declared := &Resources{Cpu: 1000, Memory: 512}

	if got := Charge(declared, nil); got.GetCpu() != 1000 || got.GetMemory() != 512 {
		t.Errorf("Charge() with no overhead = %+v, want what was declared", got)
	}

	if got := Charge(declared, &Resources{Memory: 32}); got.GetCpu() != 1000 || got.GetMemory() != 544 {
		t.Errorf("Charge() with 32 MiB overhead = %+v, want 1000 and 544", got)
	}
}

// A node's executor is found by its name, and one it does not run is nil.
func TestNodeRegisterRequest_Executor(t *testing.T) {
	node := &NodeRegisterRequest{Executors: []*NodeExecutor{
		{Name: ExecutorContainerd},
		{Name: ExecutorFirecracker, Overhead: &Resources{Memory: 32}},
	}}

	if got := node.Executor(ExecutorFirecracker); got.GetOverhead().GetMemory() != 32 {
		t.Errorf("Executor(firecracker) = %+v, want its 32 MiB overhead", got)
	}

	if got := (&NodeRegisterRequest{}).Executor(ExecutorFirecracker); got != nil {
		t.Errorf("Executor() on a node without it = %+v, want nil", got)
	}
}

// A status reads back with its state, and one naming no state is refused
// rather than read as a state nothing moves to.
func TestExecutionStatus_Execution(t *testing.T) {
	var id execution.ID

	now := time.Now()

	st, err := (&ExecutionStatus{State: string(execution.StateRunning), ProviderId: "p"}).Execution(id, now)
	if err != nil || st.State != execution.StateRunning || st.ProviderID != "p" || !st.UpdatedAt.Equal(now) {
		t.Errorf("Execution() = %+v, %v; want running as observed now", st, err)
	}

	if _, err := (&ExecutionStatus{State: "paused"}).Execution(id, now); err == nil {
		t.Error("Execution() of an unknown state = nil, want an error")
	}
}
