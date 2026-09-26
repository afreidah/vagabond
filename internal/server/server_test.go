// -------------------------------------------------------------------------------
// Server Tests
//
// Author: Alex Freidah
//
// The API end to end over httptest, against fake providers, the memory ledger
// and execution stores, and a mocked job store. The Postgres stores are covered
// by the integration suite.
// -------------------------------------------------------------------------------

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/state/memory"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// testConfig is one provider that answers inside Submit and one that runs
// until cancelled, so both a finished run and a cancel can be observed.
const testConfig = `
provider "fn" { type = "fake-function" }
provider "box" { type = "fake-container" }
`

// helloJob is a parameterized function job routed to the synchronous provider.
const helloJob = `
job "hello" {
  type = "batch"

  parameterized {
    meta_required = ["version"]
  }

  routing {
    providers = ["fn"]
  }

  task "greet" {
    driver = "function"

    config {
      function = "hello-${meta.version}"
    }
  }
}
`

// sleepyJob runs on the container provider, which never finishes on its own.
const sleepyJob = `
job "sleepy" {
  type = "batch"

  routing {
    providers = ["box"]
  }

  task "wait" {
    driver = "container"

    config {
      image = "alpine:3.20"
    }
  }
}
`

// harness is a server under test and the mocked job store behind it.
type harness struct {
	url  string
	jobs *MockserverJobs
}

// fixtures builds the registry from testConfig, refreshed, and an empty
// memory ledger over it.
func fixtures(t *testing.T) (*registry.Registry, *ledger.Ledger) {
	t.Helper()

	ctx := context.Background()

	cfg, diags := config.Load("test.hcl", []byte(testConfig))
	if diags.HasErrors() {
		t.Fatalf("config: %s", diags.Error())
	}

	reg, diags := registry.New(ctx, cfg)
	if diags.HasErrors() {
		t.Fatalf("registry: %s", diags.Error())
	}

	if err := reg.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() = %v", err)
	}

	led, err := ledger.New(ctx, reg.Budgets(), ledger.NewMemory(nil))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}

	return reg, led
}

// newExecutionID mints an execution ID.
func newExecutionID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return id
}

// newHarness starts a server over the fixtures and returns its URL.
func newHarness(t *testing.T) *harness {
	t.Helper()

	reg, led := fixtures(t)
	jobStore := NewMockserverJobs(gomock.NewController(t))
	logger := slog.New(slog.DiscardHandler)

	srv := httptest.NewServer(New(reg, led, memory.NewExecutions(), jobStore, logger).Handler())
	t.Cleanup(srv.Close)

	return &harness{url: srv.URL, jobs: jobStore}
}

// call sends body as JSON and decodes the response into out, returning the
// status.
func (h *harness) call(t *testing.T, method, path string, body, out any) int {
	t.Helper()

	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encoding: %v", err)
		}
	}

	req, err := http.NewRequestWithContext(t.Context(), method, h.url+path, &buf)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decoding %s %s: %v", method, path, err)
		}
	}

	return resp.StatusCode
}

// awaitExecution polls a dispatch until its first execution reaches state.
func (h *harness) awaitExecution(t *testing.T, dispatch, state string) api.Execution {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		var d api.Dispatch

		h.call(t, http.MethodGet, "/v1/dispatch/"+dispatch, nil, &d)

		if len(d.Executions) > 0 && d.Executions[0].State == state {
			return d.Executions[0]
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("dispatch %s never reached %s", dispatch, state)

	return api.Execution{}
}

// -------------------------------------------------------------------------
// JOBS
// -------------------------------------------------------------------------

// Registering validates the job, stores it in the requested namespace, and
// answers with the version.
func TestRegister(t *testing.T) {
	h := newHarness(t)

	h.jobs.EXPECT().
		Register(gomock.Any(), "default", "hello", []byte(helloJob), gomock.Any()).
		Return(int64(1), true, nil)

	var resp api.RegisterResponse

	if status := h.call(t, http.MethodPost, "/v1/jobs", api.RegisterRequest{Source: helloJob}, &resp); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	if resp.Name != "hello" || resp.Version != 1 || !resp.Changed {
		t.Errorf("response = %+v", resp)
	}
}

// A job that does not validate is refused with its diagnostics, and nothing is
// stored.
func TestRegister_InvalidJob(t *testing.T) {
	h := newHarness(t)

	var resp api.Error

	status := h.call(t, http.MethodPost, "/v1/jobs", api.RegisterRequest{Source: `job "x" { type = "nonsense" }`}, &resp)

	if status != http.StatusBadRequest || len(resp.Diagnostics) == 0 {
		t.Errorf("status = %d, body = %+v; want 400 with diagnostics", status, resp)
	}
}

// A stopped job cannot be dispatched until it is registered again.
func TestDispatch_StoppedJob(t *testing.T) {
	h := newHarness(t)

	h.jobs.EXPECT().Job(gomock.Any(), "default", "hello").
		Return(&jobs.Job{Namespace: "default", Name: "hello", Version: 1, Stopped: true}, nil)

	status := h.call(t, http.MethodPost, "/v1/job/hello/dispatch", api.DispatchRequest{}, nil)

	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409", status)
	}
}

// An unregistered job is a 404.
func TestDispatch_UnknownJob(t *testing.T) {
	h := newHarness(t)

	h.jobs.EXPECT().Job(gomock.Any(), "default", "absent").Return(nil, jobs.ErrNotFound)

	if status := h.call(t, http.MethodPost, "/v1/job/absent/dispatch", api.DispatchRequest{}, nil); status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}

// -------------------------------------------------------------------------
// DISPATCH
// -------------------------------------------------------------------------

// A dispatch answers at once with its ID, and its execution is readable by
// dispatch and by ID once it finishes.
func TestDispatch_RunsAndIsReadable(t *testing.T) {
	h := newHarness(t)

	h.jobs.EXPECT().Job(gomock.Any(), "default", "hello").
		Return(&jobs.Job{Namespace: "default", Name: "hello", Version: 2}, nil)
	h.jobs.EXPECT().Version(gomock.Any(), "default", "hello", int64(2)).
		Return(&jobs.Version{Namespace: "default", Name: "hello", Version: 2, Source: []byte(helloJob)}, nil)

	var started api.DispatchResponse

	req := api.DispatchRequest{Meta: map[string]string{"version": "1"}}
	if status := h.call(t, http.MethodPost, "/v1/job/hello/dispatch", req, &started); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	exec := h.awaitExecution(t, started.DispatchID, "succeeded")

	if exec.JobVersion != 2 || exec.DispatchID != started.DispatchID || !exec.HasResult {
		t.Errorf("execution = %+v", exec)
	}

	var byID api.Execution

	if status := h.call(t, http.MethodGet, "/v1/execution/"+exec.ID, nil, &byID); status != http.StatusOK || byID.ID != exec.ID {
		t.Errorf("GET execution = %d, %+v", status, byID)
	}
}

// Metadata the job does not declare is refused before anything runs.
func TestDispatch_UndeclaredMetadata(t *testing.T) {
	h := newHarness(t)

	h.jobs.EXPECT().Job(gomock.Any(), "default", "hello").
		Return(&jobs.Job{Namespace: "default", Name: "hello", Version: 1}, nil)
	h.jobs.EXPECT().Version(gomock.Any(), "default", "hello", int64(1)).
		Return(&jobs.Version{Version: 1, Source: []byte(helloJob)}, nil)

	req := api.DispatchRequest{Meta: map[string]string{"version": "1", "branch": "main"}}

	if status := h.call(t, http.MethodPost, "/v1/job/hello/dispatch", req, nil); status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

// A job file runs without being registered.
func TestRun_FileRunsWithoutRegistering(t *testing.T) {
	h := newHarness(t)

	var started api.DispatchResponse

	req := api.RunRequest{Source: helloJob, Meta: map[string]string{"version": "1"}}
	if status := h.call(t, http.MethodPost, "/v1/jobs/run", req, &started); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	if exec := h.awaitExecution(t, started.DispatchID, "succeeded"); exec.JobVersion != 0 {
		t.Errorf("JobVersion = %d, want 0 for a file", exec.JobVersion)
	}
}

// A run nothing can take has no executions; its dispatch says it got no
// answer, and why.
func TestRun_RefusedRunIsVisible(t *testing.T) {
	h := newHarness(t)

	nowhere := strings.Replace(sleepyJob, `providers = ["box"]`, `providers = ["fn"]`, 1)

	var started api.DispatchResponse

	if status := h.call(t, http.MethodPost, "/v1/jobs/run", api.RunRequest{Source: nowhere}, &started); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		var d api.Dispatch

		h.call(t, http.MethodGet, "/v1/dispatch/"+started.DispatchID, nil, &d)

		if d.State == "unanswered" {
			if d.Error == "" || len(d.Executions) != 0 || d.Ended == nil {
				t.Errorf("dispatch = %+v, want a reason, an end, and no executions", d)
			}

			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("the refused run never read as unanswered")
}

// A dispatch nothing recorded is a 404.
func TestDispatch_UnknownID(t *testing.T) {
	h := newHarness(t)

	if status := h.call(t, http.MethodGet, "/v1/dispatch/01a0dd13-4d4a-7c25-82a1-772b021746b8", nil, nil); status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}

// Cancelling a running execution stops its dispatch, and the record says so.
func TestCancel_StopsARunningDispatch(t *testing.T) {
	h := newHarness(t)

	var started api.DispatchResponse

	if status := h.call(t, http.MethodPost, "/v1/jobs/run", api.RunRequest{Source: sleepyJob}, &started); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	exec := h.awaitExecution(t, started.DispatchID, "accepted")

	if status := h.call(t, http.MethodDelete, "/v1/execution/"+exec.ID, nil, nil); status != http.StatusOK {
		t.Fatalf("DELETE status = %d", status)
	}

	h.awaitExecution(t, started.DispatchID, "cancelled")
}

// -------------------------------------------------------------------------
// PLANS AND ERRORS
// -------------------------------------------------------------------------

// A plan names the selected provider and why the others were refused.
func TestPlan_File(t *testing.T) {
	h := newHarness(t)

	var plan api.Plan

	req := api.PlanRequest{Source: helloJob, Meta: map[string]string{"version": "1"}}
	if status := h.call(t, http.MethodPost, "/v1/jobs/plan", req, &plan); status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	if len(plan.Tasks) != 1 || plan.Tasks[0].Selected != "fn" || len(plan.Tasks[0].Rejections) != 1 {
		t.Errorf("plan = %+v, want fn selected and box rejected", plan)
	}
}

// An execution ID nothing recorded is a 404, and one that is not an ID a 400.
func TestExecution_Missing(t *testing.T) {
	h := newHarness(t)

	if status := h.call(t, http.MethodGet, "/v1/execution/01a0dd13-4d4a-7c25-82a1-772b021746b8", nil, nil); status != http.StatusNotFound {
		t.Errorf("unknown ID status = %d, want 404", status)
	}

	if status := h.call(t, http.MethodGet, "/v1/execution/nope", nil, nil); status != http.StatusBadRequest {
		t.Errorf("malformed ID status = %d, want 400", status)
	}
}

// A namespace the configuration does not declare is refused.
func TestNamespace_Undeclared(t *testing.T) {
	h := newHarness(t)

	var resp api.Error

	status := h.call(t, http.MethodGet, "/v1/jobs?namespace=nowhere", nil, &resp)

	if status != http.StatusBadRequest || !strings.Contains(resp.Error, "not declared") {
		t.Errorf("status = %d, body = %+v", status, resp)
	}
}

// -------------------------------------------------------------------------
// LIFECYCLE
// -------------------------------------------------------------------------

// A dispatch whose owner died mid-execution is claimed at startup, polled to
// its end, and recorded as finished by this server.
func TestServeListener_ResumesAnAbandonedDispatch(t *testing.T) {
	ctx := t.Context()

	reg, led := fixtures(t)
	executions := memory.NewExecutions()

	box, _ := reg.Provider("box")
	lapsed := time.Now().Add(-time.Hour)

	d := &execution.Dispatch{
		ID: newExecutionID(t), Namespace: "default", Job: "sleepy", Tasks: 1,
		State: execution.DispatchRunning, Owner: "cli:dead:1", LeaseUntil: lapsed, Created: lapsed,
	}
	if err := executions.CreateDispatch(ctx, d); err != nil {
		t.Fatalf("CreateDispatch() = %v", err)
	}

	id := newExecutionID(t)
	if _, err := box.Submit(ctx, id, &job.Task{Name: "wait"}); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	for _, next := range []execution.State{execution.StateRunning, execution.StateSucceeded} {
		if err := box.(*plugin.FakeContainerProvider).Advance(id, next); err != nil {
			t.Fatalf("Advance(%s) = %v", next, err)
		}
	}

	err := executions.Create(ctx, &execution.Record{
		Status:    execution.Status{ID: id, State: execution.StateRunning, UpdatedAt: lapsed},
		Namespace: "default", Job: "sleepy", Dispatch: d.ID, Task: "wait", Provider: "box", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := New(reg, led, executions, NewMockserverJobs(gomock.NewController(t)), slog.New(slog.DiscardHandler))

	serveCtx, stop := context.WithCancel(ctx)
	defer stop()

	go func() { _ = srv.ServeListener(serveCtx, listener, nil) }()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		got, err := executions.GetDispatch(ctx, d.ID)
		if err == nil && got.State != execution.DispatchRunning {
			if got.State != execution.DispatchSucceeded || got.Owner != srv.owner {
				t.Errorf("dispatch = %+v, want succeeded under this server", got)
			}

			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("the abandoned dispatch was never finished")
}

// The server stops cleanly when its context is cancelled.
func TestServeListener_StopsOnCancel(t *testing.T) {
	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	led, err := ledger.New(t.Context(), quota.Budgets{}, ledger.NewMemory(nil))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}

	ctrl := gomock.NewController(t)
	srv := New(NewMockserverRegistry(ctrl), led, memory.NewExecutions(), NewMockserverJobs(ctrl),
		slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- srv.ServeListener(ctx, listener, nil) }()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ServeListener() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
}
