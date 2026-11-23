// -------------------------------------------------------------------------------
// Cloud Build Provider Tests
//
// Author: Alex Freidah
//
// Against the fake Google: the build Submit sends, finding a build by its tag,
// how each build status reads, what Result reports, a cancel, and the log
// filter. A build Google ended itself must come back as infrastructure, so the
// task is tried on another provider.
// -------------------------------------------------------------------------------

package cloudbuild

import (
	"bytes"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/plugin/plugintest"
	"github.com/afreidah/vagabond/internal/providers/gcp/gcptest"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// buildsPath is the test project's build collection.
const buildsPath = "/v1/projects/test-project/locations/us-central1/builds"

// newFake starts a fake Google and a Cloud Build provider pointed at it.
func newFake(t *testing.T) (*gcptest.Google, *Provider) {
	t.Helper()

	g, api := gcptest.NewGoogle(t, "gcp-cloud-build")

	return g, &Provider{API: api, buildURL: g.URL}
}

// listing is a builds:list response holding one build.
func listing(build map[string]any) map[string]any {
	return map[string]any{"builds": []any{build}}
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// A task becomes a build of one step: its image, entrypoint, args and sorted
// env, with its timeout, the execution's tag, the runtime identity, the free
// machine and logging to Cloud Logging only. The build's id comes back.
func TestSubmitSendsOneStep(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("POST "+buildsPath, map[string]any{"metadata": map[string]any{"build": map[string]any{"id": "b-1"}}})

	id := testkit.NewID(t)

	submission, err := p.Submit(t.Context(), id, plugintest.ContainerTask(t))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	if submission.ProviderID != "b-1" || submission.State != execution.StateAccepted {
		t.Errorf("submission = %+v, want build b-1 accepted", submission)
	}

	want := map[string]any{
		"steps": []any{map[string]any{
			"name":       "golang:1.27",
			"entrypoint": "go",
			"args":       []any{"test", "./..."},
			"env":        []any{"CGO_ENABLED=0", "CI=true"},
		}},
		"timeout":        "900s",
		"tags":           []any{"vagabond-" + id.String()},
		"serviceAccount": "projects/test-project/serviceAccounts/runtime@test-project.iam.gserviceaccount.com",
		"options":        map[string]any{"machineType": "E2_STANDARD_2", "logging": "CLOUD_LOGGING_ONLY"},
	}

	if diff := cmp.Diff(want, g.Request(0).Body); diff != "" {
		t.Errorf("build (-want +got):\n%s", diff)
	}
}

// Cloud Build reads $NAME in a step's args and env as its own substitution,
// refusing one it does not know and filling in its built-in ones, so every $
// there goes out doubled, which it hands the container as one.
func TestSubmitDoublesDollars(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("POST "+buildsPath, map[string]any{"metadata": map[string]any{"build": map[string]any{"id": "b-1"}}})

	task := &job.Task{
		Name:   "test",
		Driver: job.DriverContainer,
		Config: plugintest.RawBlock(t, `
image   = "alpine:3.20"
command = "sh"
args    = ["-ec", "echo $REPO"]
`),
		Env: plugintest.RawBlock(t, `ID = "$BUILD_ID"`+"\n"),
	}

	if _, err := p.Submit(t.Context(), testkit.NewID(t), task); err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	step := g.Request(0).Body["steps"].([]any)[0].(map[string]any)

	if diff := cmp.Diff([]any{"-ec", "echo $$REPO"}, step["args"]); diff != "" {
		t.Errorf("args (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff([]any{"ID=$$BUILD_ID"}, step["env"]); diff != "" {
		t.Errorf("env (-want +got):\n%s", diff)
	}

	// Not shown substituted in Google's docs, so sent as written
	if step["entrypoint"] != "sh" {
		t.Errorf("entrypoint = %v, want sh", step["entrypoint"])
	}
}

// A task Cloud Build cannot run is refused before any call, and a refused
// build comes back as the API's error.
func TestSubmitRefusals(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)

	noImage := &job.Task{Name: "test", Driver: job.DriverContainer, Config: plugintest.RawBlock(t, `command = "go"`)}
	if _, err := p.Submit(t.Context(), testkit.NewID(t), noImage); err == nil || len(g.Requests) != 0 {
		t.Errorf("Submit(no image) = %v after %d calls, want an error before any", err, len(g.Requests))
	}

	// Nothing registered, so the create 404s
	if _, err := p.Submit(t.Context(), testkit.NewID(t), plugintest.ContainerTask(t)); !errors.Is(err, plugin.ErrNotFound) {
		t.Errorf("Submit() = %v, want the API's refusal", err)
	}
}

// -------------------------------------------------------------------------
// STATUS
// -------------------------------------------------------------------------

// Status finds the build by the execution's tag and reports its state and
// times, with the build's id as the provider's.
func TestStatusFindsTheBuildByTag(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("GET "+buildsPath, listing(map[string]any{"id": "b-1", "status": "WORKING", "startTime": "2026-11-22T10:00:00Z"}))

	id := testkit.NewID(t)

	status, err := p.Status(t.Context(), id)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if status.State != execution.StateRunning || status.ProviderID != "b-1" ||
		!status.StartedAt.Equal(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("status = %+v, want b-1 running since 10:00", status)
	}

	query, _ := url.ParseQuery(g.Request(0).Query)
	if want := `tags="vagabond-` + id.String() + `"`; query.Get("filter") != want {
		t.Errorf("filter = %q, want %q", query.Get("filter"), want)
	}
}

// Every status Cloud Build reports reads as a Vagabond state. One the plugin
// does not know is internal, since guessing could misreport a finished run.
func TestEveryStatusReads(t *testing.T) {
	t.Parallel()

	states := map[string]execution.State{
		"PENDING":        execution.StateAccepted,
		"QUEUED":         execution.StateAccepted,
		"WORKING":        execution.StateRunning,
		"SUCCESS":        execution.StateSucceeded,
		"FAILURE":        execution.StateFailed,
		"TIMEOUT":        execution.StateFailed,
		"INTERNAL_ERROR": execution.StateFailed,
		"EXPIRED":        execution.StateFailed,
		"CANCELLED":      execution.StateCancelled,
	}

	for status, want := range states {
		if got, err := (&buildRun{Status: status}).state(); err != nil || got != want {
			t.Errorf("%s reads as %s, %v; want %s", status, got, err, want)
		}
	}

	if _, err := (&buildRun{Status: "STATUS_UNKNOWN"}).state(); err == nil || plugin.Reroutable(err) {
		t.Errorf("an unknown status = %v, want an internal error", err)
	}
}

// No build carrying the tag is an unknown execution, kept infrastructure,
// since right after Submit the listing can lag the build.
func TestStatusWithNoBuild(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("GET "+buildsPath, map[string]any{"builds": []any{}})

	_, err := p.Status(t.Context(), testkit.NewID(t))
	if !errors.Is(err, plugin.ErrUnknownExecution) || !plugin.Reroutable(err) {
		t.Errorf("Status() = %v, want an unknown execution classified infrastructure", err)
	}
}

// -------------------------------------------------------------------------
// RESULT
// -------------------------------------------------------------------------

// Result reports the step's exit code and how long the build ran, and the
// reason when the build ran past its timeout. A build Google ended itself is
// infrastructure, so the task is tried elsewhere.
func TestResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		build   map[string]any
		code    int
		reason  string
		reroute bool
	}{
		{
			name: "failed step",
			build: map[string]any{"id": "b-1", "status": "FAILURE",
				"startTime": "2026-11-22T10:00:00Z", "finishTime": "2026-11-22T10:01:30Z",
				"steps": []any{map[string]any{"exitCode": 2}}},
			code: 2,
		},
		{
			name: "timed out",
			build: map[string]any{"id": "b-1", "status": "TIMEOUT", "statusDetail": "build timed out",
				"startTime": "2026-11-22T10:00:00Z", "finishTime": "2026-11-22T10:01:30Z",
				"steps": []any{map[string]any{"exitCode": 137}}},
			code:   137,
			reason: "build timed out",
		},
		{
			name:    "ended by Google",
			build:   map[string]any{"id": "b-1", "status": "INTERNAL_ERROR", "statusDetail": "worker lost"},
			reroute: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g, p := newFake(t)
			g.Respond("GET "+buildsPath, listing(tt.build))

			result, err := p.Result(t.Context(), testkit.NewID(t))
			if tt.reroute {
				if !plugin.Reroutable(err) {
					t.Errorf("Result() = %v, want an infrastructure error", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("Result failed: %v", err)
			}

			if result.ExitCode == nil || *result.ExitCode != tt.code || result.Duration != 90*time.Second ||
				result.Reason != tt.reason {
				t.Errorf("result = %+v, want exit %d after 90s, reason %q", result, tt.code, tt.reason)
			}
		})
	}
}

// -------------------------------------------------------------------------
// CANCEL
// -------------------------------------------------------------------------

// Cancel asks Cloud Build to stop the execution's build.
func TestCancelStopsTheBuild(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("GET "+buildsPath, listing(map[string]any{"id": "b-1", "status": "WORKING"}))
	g.Respond("POST "+buildsPath+"/b-1:cancel", map[string]any{})

	if err := p.Cancel(t.Context(), testkit.NewID(t)); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	cancel := g.Request(1)
	if cancel.Path != buildsPath+"/b-1:cancel" || cancel.Body["id"] != "b-1" || cancel.Body["projectId"] != "test-project" {
		t.Errorf("cancel = %+v, want b-1 in test-project", cancel)
	}
}

// A build no listing shows, or one already gone, has nothing to stop.
func TestCancelWithNothingToStop(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.RespondInTurn("GET "+buildsPath, map[string]any{"builds": []any{}}, listing(map[string]any{"id": "b-1"}))

	for range 2 {
		if err := p.Cancel(t.Context(), testkit.NewID(t)); err != nil {
			t.Errorf("Cancel() = %v, want nil", err)
		}
	}
}

// -------------------------------------------------------------------------
// OUTPUT
// -------------------------------------------------------------------------

// The output is read from Cloud Logging through the build's id, which the
// tag lookup found.
func TestReadLogs(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("GET "+buildsPath, listing(map[string]any{"id": "b-1", "status": "SUCCESS"}))
	g.Respond("POST /v2/entries:list", map[string]any{"entries": []any{map[string]any{"textPayload": "hello"}}})

	var out bytes.Buffer

	if err := p.ReadLogs(t.Context(), testkit.NewID(t), &execution.PlacedWriter{Writer: &out}); err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	if out.String() != "hello\n" {
		t.Errorf("output = %q, want hello", out.String())
	}

	if filter := g.Request(1).Body["filter"]; filter != `resource.type="build" resource.labels.build_id="b-1"` {
		t.Errorf("filter = %q", filter)
	}
}

// Output of a build no listing shows is the lookup's error, read or streamed.
func TestLogsWithNoBuild(t *testing.T) {
	t.Parallel()

	g, p := newFake(t)
	g.Respond("GET "+buildsPath, map[string]any{"builds": []any{}})

	id := testkit.NewID(t)

	if err := p.ReadLogs(t.Context(), id, &execution.PlacedWriter{Writer: &bytes.Buffer{}}); !errors.Is(err, plugin.ErrUnknownExecution) {
		t.Errorf("ReadLogs() = %v, want an unknown execution", err)
	}

	if err := p.StreamLogs(t.Context(), id, 0, &bytes.Buffer{}); !errors.Is(err, plugin.ErrUnknownExecution) {
		t.Errorf("StreamLogs() = %v, want an unknown execution", err)
	}
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// A usable config builds a provider against the real Cloud Build endpoint,
// publishing the one machine every build runs on.
func TestNew(t *testing.T) {
	t.Parallel()

	p, diags := New(t.Context(), "gcp-cloud-build", plugintest.Body(t, gcptest.Config), []byte(gcptest.Key))
	if diags.HasErrors() {
		t.Fatalf("New() failed: %s", diags.Error())
	}

	if p.Name() != "gcp-cloud-build" || p.buildURL != buildEndpoint {
		t.Errorf("New() = %+v, want the name and the real endpoint", p)
	}

	caps, _ := p.Capabilities(t.Context())
	if *caps.MaxResources.CPU != 2000 || *caps.MaxResources.Memory != 8192 || caps.MaxDuration != 24*time.Hour ||
		!caps.SupportsDriver(job.DriverContainer) {
		t.Errorf("capabilities = %+v, want the e2-standard-2 machine for 24 hours", caps)
	}

	if _, diags := New(t.Context(), "gcp-cloud-build", nil, []byte(gcptest.Key)); !diags.HasErrors() {
		t.Error("New() built a provider with no config block")
	}
}
