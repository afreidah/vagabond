// -------------------------------------------------------------------------------
// Wire Conversions
//
// Author: Alex Freidah
//
// The protocol's execution messages to and from the types both sides work in,
// and the room a node has left, which the agent counts before starting a
// workload and the server before placing one. Kept in one place so the two
// sides cannot disagree about either.
// -------------------------------------------------------------------------------

package agentrpc

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// ROOM
// -------------------------------------------------------------------------

// Executors a node runs workloads with, by the names they register under and
// workloads name.
const (
	ExecutorContainerd  = "containerd"
	ExecutorFirecracker = "firecracker"
)

// Free is the room a node has left, in millicores and MiB: its capacity, less
// what its running workloads take, less pending workloads it does not yet
// report running. A pending workload the node already runs counts once.
func Free(capacity *Resources, held []*Held, pending map[string]*Resources) (cpu, memory int64) {
	cpu, memory = capacity.GetCpu(), capacity.GetMemory()
	running := make(map[string]bool, len(held))

	for _, h := range held {
		// A finished workload is held until released but no longer uses the node.
		if h.GetRunning() {
			running[h.GetExecutionId()] = true
			cpu, memory = cpu-h.GetResources().GetCpu(), memory-h.GetResources().GetMemory()
		}
	}

	for id, r := range pending {
		if !running[id] {
			cpu, memory = cpu-r.GetCpu(), memory-r.GetMemory()
		}
	}

	return cpu, memory
}

// Charge is what a workload takes of a node: what it declared, and its
// executor's overhead on top.
func Charge(declared, overhead *Resources) *Resources {
	return &Resources{
		Cpu:    declared.GetCpu() + overhead.GetCpu(),
		Memory: declared.GetMemory() + overhead.GetMemory(),
	}
}

// Executor is the node's executor called name, nil when it runs none by that
// name.
func (r *NodeRegisterRequest) Executor(name string) *NodeExecutor {
	for _, e := range r.GetExecutors() {
		if e.GetName() == name {
			return e
		}
	}

	return nil
}

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// StatusOf puts an execution's status on the wire.
func StatusOf(st *execution.Status) *ExecutionStatus {
	return &ExecutionStatus{
		State:      string(st.State),
		ProviderId: st.ProviderID,
		StartedAt:  timestamp(st.StartedAt),
		EndedAt:    timestamp(st.EndedAt),
	}
}

// Execution reads the status back as execution id's, observed at now. A state
// internal/execution does not name is an error, not a status no transition
// would take.
func (s *ExecutionStatus) Execution(id execution.ID, now time.Time) (execution.Status, error) {
	state, err := execution.ParseState(s.GetState())
	if err != nil {
		return execution.Status{}, err
	}

	out := execution.Status{
		ID:         id,
		State:      state,
		ProviderID: s.GetProviderId(),
		UpdatedAt:  now,
	}

	if s.GetStartedAt() != nil {
		out.StartedAt = s.GetStartedAt().AsTime()
	}

	if s.GetEndedAt() != nil {
		out.EndedAt = s.GetEndedAt().AsTime()
	}

	return out, nil
}

// ResultOf puts a finished execution's exit code and run time on the wire.
func ResultOf(res *execution.Result) *ExecutionResult {
	out := &ExecutionResult{Duration: durationpb.New(res.Duration)}

	if res.ExitCode != nil {
		out.ExitCode = new(int32(*res.ExitCode)) //nolint:gosec // exit codes fit
	}

	return out
}

// Execution reads the result back as execution id's. An unset exit code is a
// workload that never exited, which is not the same as zero.
func (r *ExecutionResult) Execution(id execution.ID) *execution.Result {
	out := &execution.Result{ID: id, Duration: r.GetDuration().AsDuration()}

	if r.ExitCode != nil {
		out.ExitCode = new(int(r.GetExitCode()))
	}

	return out
}

// timestamp is nil for the zero time, which the wire reads as unset.
func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}
