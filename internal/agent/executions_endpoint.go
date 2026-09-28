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
	Logs(ctx context.Context, id string, w io.Writer) error
	Held(ctx context.Context) ([]executor.Held, error)
	Runtimes() []string
}

// -------------------------------------------------------------------------
// ENDPOINT
// -------------------------------------------------------------------------

// executions serves the executor to the server. capacity is what the node may
// use; mu makes checking room and starting a workload one step, and changed
// tells the agent its workloads changed and it should report them.
type executions struct {
	agentrpc.UnimplementedAgentExecutionsServer

	exec     agentExecutor
	capacity *agentrpc.Resources
	changed  chan<- struct{}
	mu       sync.Mutex
}

// Submit starts a workload that fits in what the node has left, and answers
// with the state it reached. One that does not fit is ResourceExhausted, which
// the server takes as a reason to try elsewhere.
func (e *executions) Submit(ctx context.Context, req *agentrpc.SubmitRequest) (*agentrpc.Submission, error) {
	id, spec := req.GetExecutionId(), specOf(req.GetWorkload())

	// Held across the check and the start, so two submissions racing for the
	// last room cannot both see it free.
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.fits(ctx, id, spec); err != nil {
		return nil, err
	}

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

// fits refuses a workload the node's running workloads leave no room for. A
// workload already held under id is a resubmission and always fits.
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

		// A finished workload keeps its container until released, but no
		// longer uses the node.
		if h.Running {
			cpu, memory = cpu+h.CPU, memory+h.Memory
		}
	}

	// Declared sizes, not live usage: each workload is capped at what it
	// declared, so their sum is what the node could be asked for at once.
	if cpu+spec.CPU > e.capacity.GetCpu() || memory+spec.Memory > e.capacity.GetMemory() {
		return status.Errorf(codes.ResourceExhausted,
			"the node has %d millicores and %d MiB free; the workload needs %d and %d",
			e.capacity.GetCpu()-cpu, e.capacity.GetMemory()-memory, spec.CPU, spec.Memory)
	}

	return nil
}

// notify tells the agent to report its workloads, without waiting.
func (e *executions) notify() {
	// One pending signal is enough: the report sends everything, so a second
	// signal while one waits would add nothing.
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

// Result answers with what a finished workload produced.
func (e *executions) Result(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.ExecutionResult, error) {
	res, err := e.exec.Result(ctx, req.GetExecutionId())
	if err != nil {
		return nil, failure(err)
	}

	out := &agentrpc.ExecutionResult{
		Duration:      durationpb.New(res.Duration),
		Logs:          res.Logs,
		LogsTruncated: res.LogsTruncated,
	}

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

// StreamLogs sends a workload's output as it is written, until it ends or the
// server stops listening.
func (e *executions) StreamLogs(req *agentrpc.ExecutionRequest, stream agentrpc.AgentExecutions_StreamLogsServer) error {
	return failure(e.exec.Logs(stream.Context(), req.GetExecutionId(), chunkWriter{stream}))
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
