// -------------------------------------------------------------------------------
// Cloud Run Provider Tests
//
// Author: Alex Freidah
//
// Run against an httptest server standing in for Google, so request building,
// headers, encoding and status classification are all exercised for real. A
// mocked HTTP client would assert that Do was called with the right arguments,
// which is a weaker claim than that Google would have understood us.
//
// The canned responses are shaped after what a spike actually received,
// including the exit code living on lastAttemptResult and the execution
// carrying a suffix Google chose.
// -------------------------------------------------------------------------------

package gcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
)

// recorded is one request the fake Google saw.
type recorded struct {
	method string
	path   string
	query  string
	body   map[string]any
}

// fakeGoogle stands in for Cloud Run and Cloud Logging.
//
// Handlers are keyed by "METHOD /path" with a trailing star meaning prefix, so
// a test states only the responses it cares about and anything unexpected
// fails loudly rather than returning an empty body that decodes to zeroes.
type fakeGoogle struct {
	t         *testing.T
	handlers  map[string]any
	sequences map[string][]any
	requests  []recorded
}

func newFakeGoogle(t *testing.T) (*fakeGoogle, *Provider) {
	t.Helper()

	g := &fakeGoogle{
		t:         t,
		handlers:  map[string]any{},
		sequences: map[string][]any{},
	}

	server := httptest.NewServer(g)
	t.Cleanup(server.Close)

	p := &Provider{
		name: "gcp-cloud-run",
		cfg: &Config{
			Project:               "test-project",
			Region:                "us-central1",
			RuntimeServiceAccount: "runtime@test-project.iam.gserviceaccount.com",
		},
		// The real client signs requests; a test server needs no auth, and
		// swapping it here is the point of the endpoints being fields.
		http:    server.Client(),
		runURL:  server.URL,
		logsURL: server.URL,
	}

	return g, p
}

// respond registers what the fake returns for one route.
func (g *fakeGoogle) respond(route string, body any) {
	g.handlers[route] = body
}

// respondInTurn registers successive responses for one route, for the APIs
// whose answer depends on how many times they have been asked.
func (g *fakeGoogle) respondInTurn(route string, bodies ...any) {
	g.sequences[route] = bodies
}

func (g *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	g.requests = append(g.requests, recorded{
		method: r.Method,
		path:   r.URL.Path,
		query:  r.URL.RawQuery,
		body:   body,
	})

	key := r.Method + " " + r.URL.Path

	if queued, ok := g.sequences[key]; ok && len(queued) > 0 {
		g.sequences[key] = queued[1:]

		writeJSON(w, http.StatusOK, queued[0])

		return
	}

	if response, ok := g.handlers[key]; ok {
		writeJSON(w, http.StatusOK, response)

		return
	}

	for route, response := range g.handlers {
		if strings.HasSuffix(route, "*") &&
			strings.HasPrefix(key, strings.TrimSuffix(route, "*")) {
			writeJSON(w, http.StatusOK, response)

			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{"message": "no handler for " + key},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// request returns the nth recorded request.
func (g *fakeGoogle) request(n int) recorded {
	g.t.Helper()

	if n >= len(g.requests) {
		g.t.Fatalf("wanted request %d, only %d were made", n, len(g.requests))
	}

	return g.requests[n]
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// rawBlock parses a driver config the way the job parser leaves it: decoded as
// a block, with its attributes still expressions.
func rawBlock(t *testing.T, src string) *job.RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing config failed: %s", diags.Error())
	}

	return &job.RawBlock{Body: f.Body}
}

func newID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() failed: %v", err)
	}

	return id
}

// containerTask is a task shaped like the documented example.
func containerTask(t *testing.T) *job.Task {
	t.Helper()

	return &job.Task{
		Name:      "test",
		Driver:    job.DriverContainer,
		Config:    rawBlock(t, "image = \"golang:1.27\"\ncommand = \"go\"\nargs = [\"test\", \"./...\"]\n"),
		Env:       rawBlock(t, "CI = \"true\"\nCGO_ENABLED = \"0\"\n"),
		Resources: &job.Resources{CPU: ptr.Of(1000), Memory: ptr.Of(2048)},
		Timeout:   ptr.Of(job.Duration("15m")),
	}
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// Cloud Run has no ad-hoc run, so a submission is a create followed by a run,
// and the job carries our execution id so every later call can find it.
func TestSubmitCreatesThenRuns(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond("POST /v2/projects/test-project/locations/us-central1/jobs", map[string]any{})
	g.respond("POST /v2/projects/test-project/locations/us-central1/jobs/*", map[string]any{})

	id := newID(t)

	submission, err := p.Submit(t.Context(), id, containerTask(t))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	if submission.State != execution.StateAccepted {
		t.Errorf("state = %s, want accepted", submission.State)
	}

	// Named from the execution id, which is what makes the plugin stateless: a
	// later process derives this without remembering anything.
	if want := "vagabond-" + id.String(); submission.ProviderID != want {
		t.Errorf("provider id = %q, want %q", submission.ProviderID, want)
	}

	create := g.request(0)
	if !strings.Contains(create.query, "jobId=vagabond-"+id.String()) {
		t.Errorf("create did not name the job: %q", create.query)
	}

	run := g.request(1)
	if !strings.HasSuffix(run.path, ":run") {
		t.Errorf("second call was %s, want :run", run.path)
	}
}

// Google's default is three retries, which would run a failing job four times
// and charge four times while the ledger recorded one execution.
func TestSubmitDisablesGoogleRetries(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond("POST /v2/projects/test-project/locations/us-central1/jobs", map[string]any{})
	g.respond("POST /v2/projects/test-project/locations/us-central1/jobs/*", map[string]any{})

	if _, err := p.Submit(t.Context(), newID(t), containerTask(t)); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	inner := taskTemplate(t, g.request(0).body)

	if got, ok := inner["maxRetries"].(float64); !ok || got != 0 {
		t.Errorf("maxRetries = %v, want an explicit 0", inner["maxRetries"])
	}

	// And the container runs as an identity holding nothing, not as the one
	// Vagabond authenticates with.
	if got := inner["serviceAccount"]; got != "runtime@test-project.iam.gserviceaccount.com" {
		t.Errorf("serviceAccount = %v, want the runtime identity", got)
	}
}

// The task's own settings have to survive translation, since a provider that
// quietly ran something else would be the worst kind of bug.
func TestSubmitTranslatesTheTask(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond("POST /v2/projects/test-project/locations/us-central1/jobs", map[string]any{})
	g.respond("POST /v2/projects/test-project/locations/us-central1/jobs/*", map[string]any{})

	if _, err := p.Submit(t.Context(), newID(t), containerTask(t)); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	inner := taskTemplate(t, g.request(0).body)

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

	// Sorted, so two submissions of one task are byte-identical rather than
	// differing by map iteration order.
	env := container["env"].([]any)
	if env[0].(map[string]any)["name"] != "CGO_ENABLED" {
		t.Errorf("env is not sorted: %v", env)
	}
}

// A job that was created but could not be run leaves a resource behind, so it
// is cleaned up rather than left for the sweep.
func TestSubmitCleansUpAfterAFailedRun(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond("POST /v2/projects/test-project/locations/us-central1/jobs", map[string]any{})

	if _, err := p.Submit(t.Context(), newID(t), containerTask(t)); err == nil {
		t.Fatal("expected the run to fail")
	}

	last := g.request(len(g.requests) - 1)
	if last.method != http.MethodDelete {
		t.Errorf("last call was %s, want a DELETE cleaning up", last.method)
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
		// The container has exited but Cloud Run has not written a completion
		// time yet. On the counter alone this reads as never having started.
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

			g, p := newFakeGoogle(t)
			g.respond("GET /v2/projects/test-project/locations/us-central1/jobs/*",
				map[string]any{"executions": []any{tc.execution}})

			id := newID(t)

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

	g, p := newFakeGoogle(t)
	g.respond("GET /v2/projects/test-project/locations/us-central1/jobs/*",
		map[string]any{"executions": []any{}})

	_, err := p.Status(t.Context(), newID(t))
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

	_, p := newFakeGoogle(t)

	_, err := p.Status(t.Context(), newID(t))
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

	g, p := newFakeGoogle(t)
	g.respond("GET /v2/projects/test-project/locations/us-central1/jobs/*",
		map[string]any{"executions": []any{map[string]any{
			"name":           "projects/p/locations/l/jobs/j/executions/j-nhrzk",
			"completionTime": "2026-09-20T22:55:31Z",
			"failedCount":    1,
		}}})
	g.respond("GET /v2/projects/p/locations/l/jobs/j/executions/j-nhrzk/tasks",
		map[string]any{"tasks": []any{map[string]any{
			"startTime":         "2026-09-20T22:55:23Z",
			"completionTime":    "2026-09-20T22:55:26Z",
			"lastAttemptResult": map[string]any{"exitCode": 3},
		}}})
	g.respond("POST /v2/entries:list", map[string]any{"entries": []any{
		map[string]any{"textPayload": "hello-stdout"},
		map[string]any{"textPayload": "hello-stderr"},
	}})

	result, err := p.Result(t.Context(), newID(t))
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}

	if ptr.Deref(result.ExitCode) != 3 {
		t.Errorf("exit code = %v, want 3", result.ExitCode)
	}

	if result.Succeeded() {
		t.Error("exit code 3 reported success")
	}

	if got := result.Duration; got != 3*time.Second {
		t.Errorf("duration = %s, want 3s", got)
	}

	// Both streams, interleaved in timestamp order, which is what someone
	// reading a failed build wants.
	if got := string(result.Logs); !strings.Contains(got, "hello-stdout") ||
		!strings.Contains(got, "hello-stderr") {
		t.Errorf("logs = %q, want both streams", got)
	}
}

// A run that produced an exit code and no readable output is still a usable
// answer, so a log failure must not discard the part that matters.
func TestResultSurvivesUnreadableLogs(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond("GET /v2/projects/test-project/locations/us-central1/jobs/*",
		map[string]any{"executions": []any{map[string]any{
			"name":           "projects/p/locations/l/jobs/j/executions/j-nhrzk",
			"completionTime": "2026-09-20T22:55:31Z",
			"succeededCount": 1,
		}}})
	g.respond("GET /v2/projects/p/locations/l/jobs/j/executions/j-nhrzk/tasks",
		map[string]any{"tasks": []any{map[string]any{
			"lastAttemptResult": map[string]any{"exitCode": 0},
		}}})
	// No handler for the logs endpoint, so it 404s.

	result, err := p.Result(t.Context(), newID(t))
	if err != nil {
		t.Fatalf("a log failure lost the whole result: %v", err)
	}

	if !result.Succeeded() {
		t.Error("the exit code was lost")
	}

	if len(result.Logs) != 0 {
		t.Errorf("logs = %q, want none", result.Logs)
	}
}

// -------------------------------------------------------------------------
// CANCEL
// -------------------------------------------------------------------------

func TestCancelDeletesTheJob(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond("DELETE /v2/projects/test-project/locations/us-central1/jobs/*",
		map[string]any{})

	id := newID(t)

	if err := p.Cancel(t.Context(), id); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	got := g.request(0)
	if got.method != http.MethodDelete || !strings.HasSuffix(got.path, "vagabond-"+id.String()) {
		t.Errorf("cancel called %s %s", got.method, got.path)
	}
}

// The caller wanted it not running, and it is not.
func TestCancelIsIdempotent(t *testing.T) {
	t.Parallel()

	_, p := newFakeGoogle(t)

	// Nothing registered, so the delete 404s.
	if err := p.Cancel(t.Context(), newID(t)); err != nil {
		t.Errorf("cancelling a job that is already gone failed: %v", err)
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
