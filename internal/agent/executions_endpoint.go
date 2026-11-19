// -------------------------------------------------------------------------------
// Agent Executions Endpoint
//
// Author: Alex Freidah
//
// The agent's side of AgentExecutions: the server's calls, passed to the
// executor that runs the workload. An execution no executor holds is NotFound,
// which the server reads as a workload that never reached this node.
// -------------------------------------------------------------------------------

package agent

import (
	"context"
	"errors"
	"slices"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agentrpc"
)

// executions serves the node's executors to the server. capacity is what the
// node may use, and changed tells the agent its workloads changed and it should
// report them. A submission in flight, its image pulling outside mu, holds its
// charge in reserved and a channel in inflight that closes once it is done;
// settled counts the submissions done so far.
type executions struct {
	agentrpc.UnimplementedAgentExecutionsServer

	exec     *executors
	capacity *agentrpc.Resources
	changed  chan<- struct{}

	mu       sync.Mutex
	reserved map[string]*agentrpc.Resources
	inflight map[string]chan struct{}
	settled  uint64
}

// newExecutions serves exec, which may use capacity, reporting changes on
// changed.
func newExecutions(exec *executors, capacity *agentrpc.Resources, changed chan<- struct{}) *executions {
	return &executions{
		exec:     exec,
		capacity: capacity,
		changed:  changed,
		reserved: make(map[string]*agentrpc.Resources),
		inflight: make(map[string]chan struct{}),
	}
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// Submit starts a workload on the executor it names, if it fits in what the
// node has left, and answers with the state it reached. One that does not fit
// is ResourceExhausted, which the server takes as a reason to try elsewhere.
func (e *executions) Submit(ctx context.Context, req *agentrpc.SubmitRequest) (*agentrpc.Submission, error) {
	id, w := req.GetExecutionId(), req.GetWorkload()

	// The executor first, so a workload for one the node lacks reserves nothing.
	exec, info, err := e.exec.named(w.GetExecutor())
	if err != nil {
		return nil, failure(err)
	}

	// Reserved under the lock, so racing submissions cannot share the last room.
	release, err := e.reserve(ctx, id, agentrpc.Charge(w.GetResources(), info.GetOverhead()))
	if err != nil {
		return nil, err
	}
	defer release()

	// The room stays reserved while the executor pulls the image and starts it.
	if err := exec.Submit(ctx, id, w); err != nil {
		return nil, failure(err)
	}

	// The server's picture of this node is now out of date.
	e.notify()

	st, err := exec.Status(ctx, id)
	if err != nil {
		return nil, failure(err)
	}

	return &agentrpc.Submission{State: st.GetState(), ProviderId: st.GetProviderId()}, nil
}

// reserve claims room for id, waiting when another submission for the same
// execution is already in flight, and returns a function that releases it.
// Each attempt first notes how many submissions have settled, so a claim can
// tell whether the held workloads it read since are still current.
func (e *executions) reserve(ctx context.Context, id string, need *agentrpc.Resources) (func(), error) {
	for {
		e.mu.Lock()
		seen := e.settled
		e.mu.Unlock()

		// Read outside the lock, so a slow executor holds up no other call.
		held, err := e.exec.Held(ctx)
		if err != nil {
			return nil, failure(err)
		}

		// Neither claimed nor waiting: one settled while held was read, so read again.
		done, wait, err := e.tryClaim(id, need, held, seen)

		switch {
		case err != nil:
			return nil, err
		case done != nil:
			return func() {
				e.mu.Lock()
				delete(e.reserved, id)
				delete(e.inflight, id)
				e.settled++
				e.mu.Unlock()
				close(done)
			}, nil
		case wait == nil:
			continue
		}

		// Another submission for id is in flight: wait for it, or for ctx.
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
}

// tryClaim makes one attempt to reserve room against held, read when seen
// submissions had settled, returning the channel to close once done. When
// another submission for id is in flight, it returns that submission's channel
// to wait on instead, and neither when one settled since held was read.
func (e *executions) tryClaim(
	id string, need *agentrpc.Resources, held []*agentrpc.Held, seen uint64,
) (done chan struct{}, wait <-chan struct{}, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// The same execution in flight is waited on, not claimed twice.
	if busy, ok := e.inflight[id]; ok {
		return nil, busy, nil
	}

	// One settled since held was read, so held may not show its workload.
	if e.settled != seen {
		return nil, nil, nil
	}

	if err := e.fits(id, need, held); err != nil {
		return nil, nil, err
	}

	// Closed once this submission is done, for any other of id to wait on.
	done = make(chan struct{})
	e.reserved[id], e.inflight[id] = need, done

	return done, nil, nil
}

// fits refuses a workload the node's held and reserved workloads leave no room
// for. A workload already held under id is a resubmission, which starts nothing
// new, and always fits.
func (e *executions) fits(id string, need *agentrpc.Resources, held []*agentrpc.Held) error {
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

// notify tells the agent to report its workloads, without waiting. One pending
// signal is enough, since a report sends everything.
func (e *executions) notify() {
	select {
	case e.changed <- struct{}{}:
	default:
	}
}

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// Status answers with where a workload stands, from the executor holding it.
func (e *executions) Status(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.ExecutionStatus, error) {
	var st *agentrpc.ExecutionStatus

	err := e.exec.holding(func(exec Executor) (err error) {
		st, err = exec.Status(ctx, req.GetExecutionId())

		return err
	})

	return st, failure(err)
}

// Result answers with a finished workload's exit code and run time. Its output
// is read through StreamLogs.
func (e *executions) Result(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.ExecutionResult, error) {
	var res *agentrpc.ExecutionResult

	err := e.exec.holding(func(exec Executor) (err error) {
		res, err = exec.Result(ctx, req.GetExecutionId())

		return err
	})

	return res, failure(err)
}

// Cancel stops a workload, and has the agent report the room it frees.
func (e *executions) Cancel(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.Empty, error) {
	defer e.notify()

	err := e.exec.holding(func(exec Executor) error { return exec.Cancel(ctx, req.GetExecutionId()) })

	return &agentrpc.Empty{}, failure(err)
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
	return failure(e.exec.holding(func(exec Executor) error {
		return exec.Logs(stream.Context(), req.GetExecutionId(), req.GetOffset(), chunkWriter{stream})
	}))
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// failure maps an executor error to a gRPC status: NotFound for a workload the
// node does not hold, FailedPrecondition for an executor it does not run, which
// the server takes as a reason to try another node, InvalidArgument for a
// workload no node could run, and Internal otherwise. Nil stays nil.
func failure(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, executor.ErrUnknown):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, errNoExecutor):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, executor.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
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
