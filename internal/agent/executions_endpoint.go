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
	"slices"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	Held(ctx context.Context) ([]*agentrpc.Held, error)
	Runtimes() []string
}

// -------------------------------------------------------------------------
// ENDPOINT
// -------------------------------------------------------------------------

// executions serves the executor to the server. capacity is what the node may
// use, and changed tells the agent its workloads changed and it should report
// them. A submission in flight, its image pulling outside mu, holds its size
// in reserved and a channel in inflight that closes once it is done.
type executions struct {
	agentrpc.UnimplementedAgentExecutionsServer

	exec     agentExecutor
	capacity *agentrpc.Resources
	changed  chan<- struct{}

	mu       sync.Mutex
	reserved map[string]*agentrpc.Resources
	inflight map[string]chan struct{}
}

// newExecutions serves exec, which may use capacity, reporting changes on
// changed.
func newExecutions(exec agentExecutor, capacity *agentrpc.Resources, changed chan<- struct{}) *executions {
	return &executions{
		exec:     exec,
		capacity: capacity,
		changed:  changed,
		reserved: make(map[string]*agentrpc.Resources),
		inflight: make(map[string]chan struct{}),
	}
}

// Submit starts a workload that fits in what the node has left, and answers
// with the state it reached. One that does not fit is ResourceExhausted, which
// the server takes as a reason to try elsewhere.
func (e *executions) Submit(ctx context.Context, req *agentrpc.SubmitRequest) (*agentrpc.Submission, error) {
	id, spec := req.GetExecutionId(), specOf(req.GetWorkload())

	// Reserved under the lock, so racing submissions cannot share the last room.
	release, err := e.reserve(ctx, id, req.GetWorkload().GetResources())
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
func (e *executions) reserve(ctx context.Context, id string, need *agentrpc.Resources) (func(), error) {
	for {
		done, wait, err := e.tryClaim(ctx, id, need)
		if err != nil {
			return nil, err
		}

		if done != nil {
			return func() {
				e.mu.Lock()
				delete(e.reserved, id)
				delete(e.inflight, id)
				e.mu.Unlock()
				close(done)
			}, nil
		}

		select {
		case <-wait:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
}

// tryClaim makes one attempt to reserve room, returning the channel to close
// once done. When another submission for id is in flight, it returns that
// submission's channel to wait on instead.
func (e *executions) tryClaim(
	ctx context.Context, id string, need *agentrpc.Resources,
) (done chan struct{}, wait <-chan struct{}, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if busy, ok := e.inflight[id]; ok {
		return nil, busy, nil
	}

	if err := e.fits(ctx, id, need); err != nil {
		return nil, nil, err
	}

	done = make(chan struct{})
	e.reserved[id], e.inflight[id] = need, done

	return done, nil, nil
}

// fits refuses a workload the node's running and reserved workloads leave no
// room for. A workload already held under id is a resubmission and always fits.
func (e *executions) fits(ctx context.Context, id string, need *agentrpc.Resources) error {
	held, err := e.exec.Held(ctx)
	if err != nil {
		return failure(err)
	}

	// Submitting the same execution twice starts nothing new.
	if slices.ContainsFunc(held, func(h *agentrpc.Held) bool { return h.GetExecutionId() == id }) {
		return nil
	}

	// Declared sizes, not live usage, since each workload is capped at its own.
	cpu, memory := agentrpc.Free(e.capacity, held, e.reserved)
	if need.GetCpu() > cpu || need.GetMemory() > memory {
		return status.Errorf(codes.ResourceExhausted,
			"the node has %d millicores and %d MiB free; the workload needs %d and %d",
			cpu, memory, need.GetCpu(), need.GetMemory())
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

	return agentrpc.StatusOf(&st), nil
}

// Result answers with a finished workload's exit code and run time. Its output
// is read through StreamLogs.
func (e *executions) Result(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.ExecutionResult, error) {
	res, err := e.exec.Result(ctx, req.GetExecutionId())
	if err != nil {
		return nil, failure(err)
	}

	return agentrpc.ResultOf(res), nil
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
