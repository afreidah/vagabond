// -------------------------------------------------------------------------------
// Cloud Run Provider Tests
//
// Author: Alex Freidah
//
// Run against the fake Google, so request building, headers, encoding and
// status classification are all exercised for real. The canned responses are
// shaped after real ones, including the exit code on lastAttemptResult and the
// suffix Google adds to an execution's name.
// -------------------------------------------------------------------------------

package cloudrun

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/plugin/plugintest"
	"github.com/afreidah/vagabond/internal/providers/gcp/gcptest"
	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// newFake starts a fake Google and a Cloud Run provider pointed at it.
func newFake(t *testing.T) (*gcptest.Google, *Provider) {
	t.Helper()

	g, api := gcptest.NewGoogle(t, "gcp-cloud-run")

	return g, &Provider{API: api, runURL: g.URL}
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// Cloud Run has no ad-hoc run, so a submission is a create followed by a run,
// and the job carries the execution id so every later call can find it.
func TestSubmitCreatesThenRuns(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("POST /v2/projects/test-project/locations/us-central1/jobs", map[string]any{})
	g.Respond("POST /v2/projects/test-project/locations/us-central1/jobs/*", map[string]any{})

	id := testkit.NewID(t)

	submission, err := p.Submit(t.Context(), id, plugintest.ContainerTask(t))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	if submission.State != execution.StateAccepted {
		t.Errorf("state = %s, want accepted", submission.State)
	}

	// Named from the execution id, so a later process derives it
	if want := "vagabond-" + id.String(); submission.ProviderID != want {
		t.Errorf("provider id = %q, want %q", submission.ProviderID, want)
	}

	create := g.Request(0)
	if !strings.Contains(create.Query, "jobId=vagabond-"+id.String()) {
		t.Errorf("create did not name the job: %q", create.Query)
	}

	run := g.Request(1)
	if !strings.HasSuffix(run.Path, ":run") {
		t.Errorf("second call was %s, want :run", run.Path)
	}
}

// Google's default is three retries, which would run a failing job four times
// and charge four times while the ledger recorded one execution.
func TestSubmitDisablesGoogleRetries(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("POST /v2/projects/test-project/locations/us-central1/jobs", map[string]any{})
	g.Respond("POST /v2/projects/test-project/locations/us-central1/jobs/*", map[string]any{})

	if _, err := p.Submit(t.Context(), testkit.NewID(t), plugintest.ContainerTask(t)); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	inner := taskTemplate(t, g.Request(0).Body)

	if got, ok := inner["maxRetries"].(float64); !ok || got != 0 {
		t.Errorf("maxRetries = %v, want an explicit 0", inner["maxRetries"])
	}

	// The container runs as the runtime identity, not the dispatcher's
	if got := inner["serviceAccount"]; got != "runtime@test-project.iam.gserviceaccount.com" {
		t.Errorf("serviceAccount = %v, want the runtime identity", got)
	}
}

// The task's own settings have to survive translation, since a provider that
// quietly ran something else would be the worst kind of bug.
func TestSubmitTranslatesTheTask(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("POST /v2/projects/test-project/locations/us-central1/jobs", map[string]any{})
	g.Respond("POST /v2/projects/test-project/locations/us-central1/jobs/*", map[string]any{})

	if _, err := p.Submit(t.Context(), testkit.NewID(t), plugintest.ContainerTask(t)); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	inner := taskTemplate(t, g.Request(0).Body)

	if got := inner["timeout"]; got != "900s" {
		t.Errorf("timeout = %v, want 900s", got)
	}

	container := inner["containers"].([]any)[0].(map[string]any)

	if got := container["image"]; got != "golang:1.27" {
		t.Errorf("image = %v", got)
	}

	if diff := cmp.Diff([]any{"go"}, container["command"]); diff != "" {
		t.Errorf("command mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff([]any{"test", "./..."}, container["args"]); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}

	limits := container["resources"].(map[string]any)["limits"].(map[string]any)
	if limits["cpu"] != "1" || limits["memory"] != "2048Mi" {
		t.Errorf("limits = %v, want 1 vCPU and 2048Mi", limits)
	}

	// Sorted, so two submissions of one task are byte-identical
	env := container["env"].([]any)
	if env[0].(map[string]any)["name"] != "CGO_ENABLED" {
		t.Errorf("env is not sorted: %v", env)
	}
}

// A job that was created but could not be run leaves a resource behind, so it
// is cleaned up rather than left for the release loop.
func TestSubmitCleansUpAfterAFailedRun(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("POST /v2/projects/test-project/locations/us-central1/jobs", map[string]any{})

	if _, err := p.Submit(t.Context(), testkit.NewID(t), plugintest.ContainerTask(t)); err == nil {
		t.Fatal("expected the run to fail")
	}

	last := g.Request(len(g.Requests) - 1)
	if last.Method != http.MethodDelete {
		t.Errorf("last call was %s, want a DELETE cleaning up", last.Method)
	}
}

// -------------------------------------------------------------------------
// STATUS
// -------------------------------------------------------------------------

// Cloud Run generates the execution name, so it is listed rather than derived.
func TestStatusListsTheExecution(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		execution map[string]any
		want      execution.State
	}{
		"provisioning": {
			execution: map[string]any{"name": "jobs/x/executions/x-nhrzk"},
			want:      execution.StateAccepted,
		},
		"running": {
			execution: map[string]any{
				"name":         "jobs/x/executions/x-nhrzk",
				"startTime":    "2026-09-20T22:53:35Z",
				"runningCount": 1,
			},
			want: execution.StateRunning,
		},
		// Exited with no completion time yet; the counter alone reads unstarted
		"draining": {
			execution: map[string]any{
				"name":         "jobs/x/executions/x-nhrzk",
				"startTime":    "2026-09-20T22:53:35Z",
				"runningCount": 0,
			},
			want: execution.StateRunning,
		},
		"succeeded": {
			execution: map[string]any{
				"name":           "jobs/x/executions/x-nhrzk",
				"completionTime": "2026-09-20T22:55:31Z",
				"succeededCount": 1,
			},
			want: execution.StateSucceeded,
		},
		"failed": {
			execution: map[string]any{
				"name":           "jobs/x/executions/x-nhrzk",
				"completionTime": "2026-09-20T22:55:31Z",
				"failedCount":    1,
			},
			want: execution.StateFailed,
		},
		"cancelled": {
			execution: map[string]any{
				"name":           "jobs/x/executions/x-nhrzk",
				"completionTime": "2026-09-20T22:55:31Z",
				"cancelledCount": 1,
			},
			want: execution.StateCancelled,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g, p := newFake(t)
			g.Respond("GET /v2/projects/test-project/locations/us-central1/jobs/*",
				map[string]any{"executions": []any{tc.execution}})

			id := testkit.NewID(t)

			status, err := p.Status(t.Context(), id)
			if err != nil {
				t.Fatalf("Status failed: %v", err)
			}

			if status.State != tc.want {
				t.Errorf("state = %s, want %s", status.State, tc.want)
			}

			if status.ID != id {
				t.Errorf("status ID = %s, want %s", status.ID, id)
			}
		})
	}
}

// A job with no execution is a provider problem rather than a Vagabond bug, so
// it classifies as infrastructure and may be rerouted.
func TestStatusWithNoExecution(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("GET /v2/projects/test-project/locations/us-central1/jobs/*",
		map[string]any{"executions": []any{}})

	_, err := p.Status(t.Context(), testkit.NewID(t))
	if err == nil {
		t.Fatal("expected an error")
	}

	var classified *plugin.Error
	if !errors.As(err, &classified) || classified.Class != plugin.ClassInfrastructure {
		t.Errorf("error = %v, want an infrastructure failure", err)
	}

	if !errors.Is(err, plugin.ErrUnknownExecution) {
		t.Errorf("error = %v, want plugin.ErrUnknownExecution", err)
	}
}

// No job at all is Submit dying before it created one, which the reaper reads
// as never having run.
func TestStatusWithNoJob(t *testing.T) {
	t.Parallel()

	_, p := newFake(t)

	_, err := p.Status(t.Context(), testkit.NewID(t))
	if !errors.Is(err, plugin.ErrUnknownExecution) {
		t.Errorf("error = %v, want plugin.ErrUnknownExecution", err)
	}
}

// -------------------------------------------------------------------------
// RESULT
// -------------------------------------------------------------------------

// The whole reason this platform was chosen: a structured exit code, not prose
// to parse out of a status message.
func TestResultReadsTheExitCode(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("GET /v2/projects/test-project/locations/us-central1/jobs/*",
		map[string]any{"executions": []any{map[string]any{
			"name":           "projects/p/locations/l/jobs/j/executions/j-nhrzk",
			"completionTime": "2026-09-20T22:55:31Z",
			"failedCount":    1,
		}}})
	g.Respond("GET /v2/projects/p/locations/l/jobs/j/executions/j-nhrzk/tasks",
		map[string]any{"tasks": []any{map[string]any{
			"startTime":         "2026-09-20T22:55:23Z",
			"completionTime":    "2026-09-20T22:55:26Z",
			"lastAttemptResult": map[string]any{"exitCode": 3},
		}}})

	result, err := p.Result(t.Context(), testkit.NewID(t))
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}

	if ptr.Deref(result.ExitCode) != 3 {
		t.Errorf("exit code = %v, want 3", result.ExitCode)
	}

	if got := result.Duration; got != 3*time.Second {
		t.Errorf("duration = %s, want 3s", got)
	}
}

// -------------------------------------------------------------------------
// OUTPUT
// -------------------------------------------------------------------------

// The output is captured through a filter naming the job, or one execution's
// output is stored as another's, both tailed and listed.
func TestLogsFilterToTheJob(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("POST /v2/entries:list", map[string]any{"entries": []any{map[string]any{"textPayload": "hello"}}})

	id := testkit.NewID(t)
	ended := make(chan struct{})
	close(ended)

	logs := &plugin.MemoryLog{}
	if err := p.CaptureLogs(t.Context(), id, logs, ended); err != nil || logs.String() != "hello\n" {
		t.Fatalf("CaptureLogs() = %q, %v; want the job's output", logs.String(), err)
	}

	want := `resource.type="cloud_run_job" resource.labels.job_name="` + plugin.ResourceName(id) + `"`
	if got := g.Request(0).Body["filter"]; got != want {
		t.Errorf("filter = %q, want %q", got, want)
	}

	// Nothing registered for entries:tail, so a capture of a running job ends at its refusal
	if err := p.CaptureLogs(t.Context(), id, logs, make(chan struct{})); err == nil || g.Request(1).Path != "/v2/entries:tail" {
		t.Errorf("CaptureLogs() = %v, want the tail's refusal", err)
	}
}

// -------------------------------------------------------------------------
// CANCEL
// -------------------------------------------------------------------------

// Cancel deletes the job named for the execution.
func TestCancelDeletesTheJob(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("DELETE /v2/projects/test-project/locations/us-central1/jobs/*",
		map[string]any{})

	id := testkit.NewID(t)

	if err := p.Cancel(t.Context(), id); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	got := g.Request(0)
	if got.Method != http.MethodDelete || !strings.HasSuffix(got.Path, "vagabond-"+id.String()) {
		t.Errorf("cancel called %s %s", got.Method, got.Path)
	}
}

// Cancelling or releasing a job that is already gone is not an error.
func TestCancelIsIdempotent(t *testing.T) {
	t.Parallel()

	_, p := newFake(t)

	// Nothing registered, so the delete 404s.
	if err := p.Cancel(t.Context(), testkit.NewID(t)); err != nil {
		t.Errorf("cancelling a job that is already gone failed: %v", err)
	}

	if err := p.Release(t.Context(), testkit.NewID(t)); err != nil {
		t.Errorf("releasing a job that is already gone failed: %v", err)
	}
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// A usable config builds a provider against the real Cloud Run endpoint,
// publishing Cloud Run's limits, and a missing one builds nothing.
func TestNew(t *testing.T) {
	t.Parallel()

	p, diags := New(t.Context(), "gcp-cloud-run", plugintest.Body(t, gcptest.Config), []byte(gcptest.Key))
	if diags.HasErrors() {
		t.Fatalf("New() failed: %s", diags.Error())
	}

	if p.Name() != "gcp-cloud-run" || p.runURL != runEndpoint {
		t.Errorf("New() = %+v, want the name and the real endpoint", p)
	}

	caps, _ := p.Capabilities(t.Context())
	if *caps.MaxResources.CPU != maxCPU || *caps.MaxResources.Memory != maxMemory ||
		caps.MaxDuration != 24*time.Hour || caps.Isolation != plugin.IsolationVM {
		t.Errorf("capabilities = %+v, want 8 vCPU and 32 GiB for 24 hours, in a VM", caps)
	}

	if _, diags := New(t.Context(), "gcp-cloud-run", nil, []byte(gcptest.Key)); !diags.HasErrors() {
		t.Error("New() built a provider with no config block")
	}
}

// -------------------------------------------------------------------------
// PLUMBING
// -------------------------------------------------------------------------

// taskTemplate digs out the inner template, which is where everything
// interesting about a job lives.
func taskTemplate(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	outer, ok := body["template"].(map[string]any)
	if !ok {
		t.Fatalf("no outer template in %v", body)
	}

	inner, ok := outer["template"].(map[string]any)
	if !ok {
		t.Fatalf("no inner template in %v", outer)
	}

	return inner
}
