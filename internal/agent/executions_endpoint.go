// -------------------------------------------------------------------------------
// Agent Executions Endpoint
//
// Author: Alex Freidah
//
// The agent's side of AgentExecutions: the server's calls, translated to and
// from the executor. An execution the executor does not hold is NotFound, which
// the server reads as a workload that never reached this node.
// -------------------------------------------------------------------------------

package agent

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// INTERFACE
// -------------------------------------------------------------------------

// agentExecutor runs the node's workloads. Anything it does not hold is
// executor.ErrUnknown.
type agentExecutor interface {
	Submit(ctx context.Context, id string, spec *executor.Spec) error
	Status(ctx context.Context, id string) (execution.Status, error)
	Result(ctx context.Context, id string) (*execution.Result, error)
	Cancel(ctx context.Context, id string) error
	Release(ctx context.Context, id string) error
	Logs(ctx context.Context, id string, offset int64, w io.Writer) error
	Held(ctx context.Context) ([]executor.Held, error)
	Runtimes() []string
}

// -------------------------------------------------------------------------
// ENDPOINT
// -------------------------------------------------------------------------

// executions serves the executor to the server. capacity is what the node may
// use; mu guards checking room and recording in-flight submissions in reserved
// while image pulls run outside the lock, and changed tells the agent its
// workloads changed and it should report them.
type executions struct {
	agentrpc.UnimplementedAgentExecutionsServer

	exec     agentExecutor
	capacity *agentrpc.Resources
	changed  chan<- struct{}
	mu       sync.Mutex
	reserved map[string]*reservation
}

// reservation holds a workload's declared size while its start is in flight
// outside mu, and closes done once the reservation is released.
type reservation struct {
	spec *executor.Spec
	done chan struct{}
}

// Submit starts a workload that fits in what the node has left, and answers
// with the state it reached. One that does not fit is ResourceExhausted, which
// the server takes as a reason to try elsewhere.
func (e *executions) Submit(ctx context.Context, req *agentrpc.SubmitRequest) (*agentrpc.Submission, error) {
	id, spec := req.GetExecutionId(), specOf(req.GetWorkload())

	// Reserved under the lock, so racing submissions cannot share the last room.
	release, err := e.reserve(ctx, id, spec)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := e.exec.Submit(ctx, id, spec); err != nil {
		return nil, failure(err)
	}

	// The server's picture of this node is now out of date.
	e.notify()

	st, err := e.exec.Status(ctx, id)
	if err != nil {
		return nil, failure(err)
	}

	return &agentrpc.Submission{State: string(st.State), ProviderId: st.ProviderID}, nil
}

// reserve claims room for id, waiting when another submission for the same
// execution is already in flight, and returns a function that releases it.
func (e *executions) reserve(ctx context.Context, id string, spec *executor.Spec) (func(), error) {
	for {
		res, wait, err := e.tryClaim(ctx, id, spec)
		if err != nil {
			return nil, err
		}
		if res != nil {
			return func() {
				e.mu.Lock()
				delete(e.reserved, id)
				e.mu.Unlock()
				close(res.done)
			}, nil
		}

		select {
		case <-wait:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
}

// tryClaim makes one attempt to reserve room. When another submission for id
// is in flight, it returns that submission's done channel to wait on.
func (e *executions) tryClaim(ctx context.Context, id string, spec *executor.Spec) (*reservation, <-chan struct{}, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if inflight, busy := e.reserved[id]; busy {
		return nil, inflight.done, nil
	}

	if err := e.fits(ctx, id, spec); err != nil {
		return nil, nil, err
	}

	res := &reservation{spec: spec, done: make(chan struct{})}
	e.reserved[id] = res

	return res, nil, nil
}

// fits refuses a workload the node's running and reserved workloads leave no
// room for. A workload already held under id is a resubmission and always fits.
func (e *executions) fits(ctx context.Context, id string, spec *executor.Spec) error {
	held, err := e.exec.Held(ctx)
	if err != nil {
		return failure(err)
	}

	var cpu, memory int64

	for _, h := range held {
		// Submitting the same execution twice starts nothing new.
		if h.ID == id {
			return nil
		}

		// Only running workloads use room; reserved ones are counted below.
		if _, reserved := e.reserved[h.ID]; h.Running && !reserved {
			cpu, memory = cpu+h.CPU, memory+h.Memory
		}
	}

	for _, r := range e.reserved {
		cpu, memory = cpu+r.spec.CPU, memory+r.spec.Memory
	}

	// Declared sizes, not live usage, since each workload is capped at its own.
	if cpu+spec.CPU > e.capacity.GetCpu() || memory+spec.Memory > e.capacity.GetMemory() {
		return status.Errorf(codes.ResourceExhausted,
			"the node has %d millicores and %d MiB free; the workload needs %d and %d",
			e.capacity.GetCpu()-cpu, e.capacity.GetMemory()-memory, spec.CPU, spec.Memory)
	}

	return nil
}

// notify tells the agent to report its workloads, without waiting.
func (e *executions) notify() {
	// One pending signal is enough, since a report sends everything.
	select {
	case e.changed <- struct{}{}:
	default:
	}
}

// Status answers with where a workload stands.
func (e *executions) Status(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.ExecutionStatus, error) {
	st, err := e.exec.Status(ctx, req.GetExecutionId())
	if err != nil {
		return nil, failure(err)
	}

	return &agentrpc.ExecutionStatus{
		State:      string(st.State),
		ProviderId: st.ProviderID,
		StartedAt:  timestamp(st.StartedAt),
		EndedAt:    timestamp(st.EndedAt),
	}, nil
}

// Result answers with a finished workload's exit code and run time. Its output
// is read through StreamLogs.
func (e *executions) Result(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.ExecutionResult, error) {
	res, err := e.exec.Result(ctx, req.GetExecutionId())
	if err != nil {
		return nil, failure(err)
	}

	out := &agentrpc.ExecutionResult{Duration: durationpb.New(res.Duration)}

	if res.ExitCode != nil {
		out.ExitCode = new(int32(*res.ExitCode)) //nolint:gosec // exit codes fit
	}

	return out, nil
}

// Cancel stops a workload, and has the agent report the room it frees.
func (e *executions) Cancel(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.Empty, error) {
	defer e.notify()

	return &agentrpc.Empty{}, failure(e.exec.Cancel(ctx, req.GetExecutionId()))
}

// Release deletes a finished workload, and has the agent report that it no
// longer holds it.
func (e *executions) Release(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.Empty, error) {
	defer e.notify()

	return &agentrpc.Empty{}, failure(e.exec.Release(ctx, req.GetExecutionId()))
}

// StreamLogs sends a workload's output from the requested offset on as it is
// written, until it ends or the server stops listening.
func (e *executions) StreamLogs(req *agentrpc.LogsRequest, stream agentrpc.AgentExecutions_StreamLogsServer) error {
	return failure(e.exec.Logs(stream.Context(), req.GetExecutionId(), req.GetOffset(), chunkWriter{stream}))
}

// -------------------------------------------------------------------------
// TRANSLATION
// -------------------------------------------------------------------------

// specOf translates a workload from the wire.
func specOf(w *agentrpc.Workload) *executor.Spec {
	return &executor.Spec{
		Image:      w.GetImage(),
		Command:    w.GetCommand(),
		Args:       w.GetArgs(),
		Env:        w.GetEnv(),
		WorkingDir: w.GetWorkingDir(),
		CPU:        w.GetResources().GetCpu(),
		Memory:     w.GetResources().GetMemory(),
		Timeout:    w.GetTimeout().AsDuration(),
		Runtime:    w.GetRuntime(),
	}
}

// timestamp is nil for the zero time, which the wire reads as unset.
func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

// failure maps an executor error to a gRPC status: NotFound for a workload
// this node does not hold, Internal otherwise. Nil stays nil.
func failure(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, executor.ErrUnknown):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// chunkWriter sends each write as a log chunk.
type chunkWriter struct {
	stream agentrpc.AgentExecutions_StreamLogsServer
}

// Write sends p, copied, since the stream may hold it past the call.
func (w chunkWriter) Write(p []byte) (int, error) {
	if err := w.stream.Send(&agentrpc.LogChunk{Data: append([]byte(nil), p...)}); err != nil {
		return 0, err
	}

	return len(p), nil
}
