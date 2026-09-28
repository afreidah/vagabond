// -------------------------------------------------------------------------------
// Client Executions Endpoint
//
// Author: Alex Freidah
//
// The client's side of ClientExecutions: the server's calls, translated to and
// from the executor. An execution the executor does not hold is NotFound, which
// the server reads as a workload that never reached this node.
// -------------------------------------------------------------------------------

package client

import (
	"context"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/client/executor"
	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// INTERFACE
// -------------------------------------------------------------------------

// clientExecutor runs the node's workloads. Anything it does not hold is
// executor.ErrUnknown.
type clientExecutor interface {
	Submit(ctx context.Context, id string, spec *executor.Spec) error
	Status(ctx context.Context, id string) (execution.Status, error)
	Result(ctx context.Context, id string) (*execution.Result, error)
	Cancel(ctx context.Context, id string) error
	Release(ctx context.Context, id string) error
	Logs(ctx context.Context, id string, w io.Writer) error
	List(ctx context.Context) ([]string, error)
	Runtimes() []string
}

// -------------------------------------------------------------------------
// ENDPOINT
// -------------------------------------------------------------------------

// executions serves the executor to the server.
type executions struct {
	agentrpc.UnimplementedClientExecutionsServer

	exec clientExecutor
}

// Submit starts a workload and answers with the state it reached.
func (e *executions) Submit(ctx context.Context, req *agentrpc.SubmitRequest) (*agentrpc.Submission, error) {
	if err := e.exec.Submit(ctx, req.GetExecutionId(), specOf(req.GetWorkload())); err != nil {
		return nil, failure(err)
	}

	st, err := e.exec.Status(ctx, req.GetExecutionId())
	if err != nil {
		return nil, failure(err)
	}

	return &agentrpc.Submission{State: string(st.State), ProviderId: st.ProviderID}, nil
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

// Cancel stops a workload.
func (e *executions) Cancel(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.Empty, error) {
	return &agentrpc.Empty{}, failure(e.exec.Cancel(ctx, req.GetExecutionId()))
}

// Release deletes a finished workload.
func (e *executions) Release(ctx context.Context, req *agentrpc.ExecutionRequest) (*agentrpc.Empty, error) {
	return &agentrpc.Empty{}, failure(e.exec.Release(ctx, req.GetExecutionId()))
}

// StreamLogs sends a workload's output as it is written, until it ends or the
// server stops listening.
func (e *executions) StreamLogs(req *agentrpc.ExecutionRequest, stream agentrpc.ClientExecutions_StreamLogsServer) error {
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
	stream agentrpc.ClientExecutions_StreamLogsServer
}

// Write sends p, copied, since the stream may hold it past the call.
func (w chunkWriter) Write(p []byte) (int, error) {
	if err := w.stream.Send(&agentrpc.LogChunk{Data: append([]byte(nil), p...)}); err != nil {
		return 0, err
	}

	return len(p), nil
}
