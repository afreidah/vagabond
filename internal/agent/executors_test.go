// -------------------------------------------------------------------------------
// Executors Tests
//
// Author: Alex Freidah
//
// The node's executors behind the endpoint: each workload reaches the executor
// it names and is charged its overhead, calls about an execution find whichever
// executor holds it, and a node lacking an executor or given a workload no node
// could run answers with a code of its own.
// -------------------------------------------------------------------------------

package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agent/executor/executortest"
	"github.com/afreidah/vagabond/internal/agentrpc"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// errSilent is an executor that does not answer.
var errSilent = errors.New("does not answer")

// silent is an executor whose Info fails, as a daemon that is not running.
type silent struct{ *executortest.Fake }

// Info fails.
func (silent) Info(context.Context) (*agentrpc.NodeExecutor, error) {
	return nil, errSilent
}

// meanwhile is an executor whose first Held read runs during before it
// returns, so what it returns is out of date by then.
type meanwhile struct {
	*executortest.Fake
	during func()
}

// Held reads the held workloads, then runs during the first time. Cleared
// before it runs, since during reads Held itself.
func (m *meanwhile) Held(ctx context.Context) ([]*agentrpc.Held, error) {
	held, err := m.Fake.Held(ctx)

	if during := m.during; during != nil {
		m.during = nil
		during()
	}

	return held, err
}

// firecracker is a fake registering as the firecracker executor, each of its
// workloads taking 32 MiB beyond what it declared.
func firecracker() *executortest.Fake {
	return &executortest.Fake{Name: agentrpc.ExecutorFirecracker, Overhead: &agentrpc.Resources{Memory: 32}}
}

// on is a submission of a 1-core workload of memory MiB to the named executor.
func on(id, name string, memory int64) *agentrpc.SubmitRequest {
	req := submitRequest(id, "alpine:3.20", 1000, memory)
	req.Workload.Executor = name

	return req
}

// codeOf is the gRPC code err carries.
func codeOf(err error) codes.Code {
	return status.Code(err)
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// Two executors under one name, or one that does not answer, stop the agent
// before it starts.
func TestExecutors_RefusesWhatItCannotRoute(t *testing.T) {
	if _, err := newExecutors(t.Context(), &executortest.Fake{}, &executortest.Fake{}); err == nil {
		t.Error("newExecutors() with two named containerd = nil, want an error")
	}

	if _, err := newExecutors(t.Context(), silent{&executortest.Fake{}}); !errors.Is(err, errSilent) {
		t.Errorf("newExecutors() with one that does not answer = %v, want its error", err)
	}
}

// A workload reaches the executor it names, and the calls after it find that
// executor; released, it is gone from both.
func TestSubmit_GoesToTheExecutorItNames(t *testing.T) {
	containerd, vm := &executortest.Fake{}, firecracker()
	ep := newTestEndpoint(t, 4000, 4096, containerd, vm)

	if _, err := ep.Submit(t.Context(), on("vm-1", agentrpc.ExecutorFirecracker, 512)); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if containerd.Workload("vm-1") != nil || vm.Workload("vm-1") == nil {
		t.Fatal("the workload did not reach the executor it named alone")
	}

	req := &agentrpc.ExecutionRequest{ExecutionId: "vm-1"}

	if st, err := ep.Status(t.Context(), req); err != nil || st.GetProviderId() != "vm-1" {
		t.Errorf("Status() = %+v, %v; want the firecracker executor's answer", st, err)
	}

	if _, err := ep.Release(t.Context(), req); err != nil || vm.Workload("vm-1") != nil {
		t.Errorf("Release() = %v; want the workload gone", err)
	}
}

// A workload naming an executor the node does not run is FailedPrecondition,
// one no node could run InvalidArgument, and an execution no executor holds
// NotFound.
func TestSubmit_AnswersWithCodesOfItsOwn(t *testing.T) {
	invalid := &executortest.Fake{Pull: func(context.Context, string) error {
		return fmt.Errorf("%w: image %q", executor.ErrInvalid, "Not A Reference")
	}}
	ep := newTestEndpoint(t, 4000, 4096, invalid)

	if _, err := ep.Submit(t.Context(), on("vm-1", agentrpc.ExecutorFirecracker, 512)); codeOf(err) != codes.FailedPrecondition {
		t.Errorf("Submit() to a missing executor = %v, want FailedPrecondition", err)
	}

	if _, err := ep.Submit(t.Context(), on("bad", agentrpc.ExecutorContainerd, 512)); codeOf(err) != codes.InvalidArgument {
		t.Errorf("Submit() of an invalid workload = %v, want InvalidArgument", err)
	}

	if _, err := ep.Status(t.Context(), &agentrpc.ExecutionRequest{ExecutionId: "nowhere"}); codeOf(err) != codes.NotFound {
		t.Errorf("Status() of an execution nothing holds = %v, want NotFound", err)
	}
}

// A workload is charged its executor's overhead, held or reserved, so one that
// fits only by what it declared is refused.
func TestSubmit_ChargesTheExecutorsOverhead(t *testing.T) {
	ep := newTestEndpoint(t, 4000, 1024, firecracker())

	if _, err := ep.Submit(t.Context(), on("too-big", agentrpc.ExecutorFirecracker, 1000)); codeOf(err) != codes.ResourceExhausted {
		t.Errorf("Submit() of 1000 MiB with 32 overhead on 1024 = %v, want ResourceExhausted", err)
	}

	if _, err := ep.Submit(t.Context(), on("fits", agentrpc.ExecutorFirecracker, 992)); err != nil {
		t.Errorf("Submit() of 992 MiB with 32 overhead on 1024 = %v, want it to fit", err)
	}
}

// A submission that settles while another reads what the node holds is not
// missed: the reader reads again rather than count room the settled one took.
func TestSubmit_ReadsAgainWhenOneSettlesMeanwhile(t *testing.T) {
	slow := &meanwhile{Fake: &executortest.Fake{}}
	ep := newTestEndpoint(t, 4000, 1000, slow)

	slow.during = func() {
		if _, err := ep.Submit(t.Context(), on("first", agentrpc.ExecutorContainerd, 600)); err != nil {
			t.Errorf("Submit() of the first = %v", err)
		}
	}

	if _, err := ep.Submit(t.Context(), on("second", agentrpc.ExecutorContainerd, 600)); codeOf(err) != codes.ResourceExhausted {
		t.Errorf("Submit() of the second = %v, want ResourceExhausted once the first is seen", err)
	}
}
