// -------------------------------------------------------------------------------
// Code Engine Provider Tests
//
// Author: Alex Freidah
//
// Run against one httptest server standing in for IAM, the v2 API and the
// Kubernetes API, whose paths do not overlap. The session is the real one, so
// every test also exchanges a key for a token. Canned responses are shaped
// after the ones a real job run produced.
// -------------------------------------------------------------------------------

package ibm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// The project, namespace and key every test runs against.
const (
	testProject   = "9d90b2a8-8b5d-4ed9-be66-9cae546ee5ca"
	testNamespace = "2ezo063n6h1a"
	testKey       = "test-api-key" //nolint:gosec // a fixture, not a credential
)

// recorded is one request the fake saw.
type recorded struct {
	method string
	path   string
	query  url.Values
	header http.Header
	form   url.Values
	body   map[string]any
}

// fakeIBM serves IAM, the v2 API and the Kubernetes API. Routes are
// "METHOD /path" with successive handlers, the last repeating; anything
// unmatched answers 404 the way the v2 API does.
type fakeIBM struct {
	t        *testing.T
	mu       sync.Mutex
	routes   map[string][]http.HandlerFunc
	requests []recorded
}

// newFakeIBM starts the fake with IAM and the kubeconfig answering, and a
// provider pointed at it.
func newFakeIBM(t *testing.T) (*fakeIBM, *Provider) {
	t.Helper()

	f := &fakeIBM{t: t, routes: map[string][]http.HandlerFunc{}}

	f.on("POST /identity/token", func(w http.ResponseWriter, r *http.Request) {
		if r.PostForm.Get("response_type") == "delegated_refresh_token" {
			writeJSON(w, http.StatusOK, map[string]any{"delegated_refresh_token": "drt"})

			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	f.on("GET /api/v1/project/"+testProject+"/config", reply(http.StatusOK, kubeconfig(testNamespace)))

	server := httptest.NewServer(f)
	t.Cleanup(server.Close)

	p := &Provider{
		name:    "ibm-code-engine",
		cfg:     &Config{Region: "us-east", ProjectID: testProject},
		iamURL:  server.URL + "/identity/token",
		apiURL:  server.URL,
		kubeURL: server.URL,
		podPoll: time.Millisecond,
	}

	auth, err := newSession(t.Context(), p.iamURL, []byte(testKey))
	if err != nil {
		t.Fatalf("newSession() = %v", err)
	}

	p.auth.Store(auth)

	return f, p
}

// on sets the handlers for one route, replacing any before.
func (f *fakeIBM) on(route string, handlers ...http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.routes[route] = handlers
}

// ServeHTTP records the request and answers from its route.
func (f *fakeIBM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(raw)))

	rec := recorded{method: r.Method, path: r.URL.Path, query: r.URL.Query(), header: r.Header.Clone()}

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		_ = r.ParseForm()
		rec.form = r.PostForm
	} else {
		_ = json.Unmarshal(raw, &rec.body)
	}

	key := r.Method + " " + r.URL.Path

	f.mu.Lock()
	f.requests = append(f.requests, rec)

	handlers := f.routes[key]
	if len(handlers) > 1 {
		f.routes[key] = handlers[1:]
	}
	f.mu.Unlock()

	if len(handlers) == 0 {
		writeJSON(w, http.StatusNotFound, v2Error("resource_not_found", "no route for "+key))

		return
	}

	handlers[0](w, r)
}

// requestsTo returns the requests made to one route, in order.
func (f *fakeIBM) requestsTo(route string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []recorded

	for _, rec := range f.requests {
		if rec.method+" "+rec.path == route {
			out = append(out, rec)
		}
	}

	return out
}

// reply answers with a JSON body and a status.
func reply(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, status, body)
	}
}

// writeJSON writes body as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// v2Error is an error body shaped the way the v2 API sends one.
func v2Error(code, message string) map[string]any {
	return map[string]any{
		"errors":      []map[string]any{{"code": code, "message": message}},
		"status_code": 0,
	}
}

// kubeconfig is the JSON kubeconfig Code Engine returns, naming one namespace.
func kubeconfig(namespace string) map[string]any {
	return map[string]any{
		"apiVersion":      "v1",
		"kind":            "Config",
		"current-context": namespace,
		"contexts": []map[string]any{{
			"name":    namespace,
			"context": map[string]any{"cluster": "proxy", "namespace": namespace, "user": namespace},
		}},
	}
}

// runPath is the v2 path of one job run.
func runPath(name string) string {
	return "/v2/projects/" + testProject + "/job_runs/" + name
}

// podsPath is the Kubernetes path of the test namespace's pods.
func podsPath() string {
	return "/api/v1/namespaces/" + testNamespace + "/pods"
}

// jobRunBody is a job run in the given status, with its one instance's
// timestamps and failure reason.
func jobRunBody(name, status, reason string) map[string]any {
	instance := map[string]any{
		"status":     status,
		"started_at": "2026-11-10T05:57:46Z",
	}

	details := map[string]any{
		"start_time":      "2026-11-10T05:57:46Z",
		"indices_details": map[string]any{"0": instance},
	}

	if status == "completed" || status == "failed" {
		instance["finished_at"] = "2026-11-10T05:58:18Z"
		details["completion_time"] = "2026-11-10T05:58:21Z"
	}

	if reason != "" {
		instance["last_failure_reason"] = reason
	}

	return map[string]any{"name": name, "status": status, "status_details": details}
}

// podBody is a job run's pod, created at created; exit is the task
// container's exit code, nil while it runs.
func podBody(name string, created time.Time, exit *int) map[string]any {
	state := map[string]any{"running": map[string]any{"startedAt": created.Format(time.RFC3339)}}
	phase := "Running"

	if exit != nil {
		state = map[string]any{"terminated": map[string]any{"exitCode": *exit, "reason": "Error"}}
		phase = "Failed"
	}

	return map[string]any{
		"metadata": map[string]any{"name": name, "creationTimestamp": created.Format(time.RFC3339)},
		"status": map[string]any{
			"phase":             phase,
			"containerStatuses": []map[string]any{{"name": userContainer, "state": state}},
		},
	}
}

// podList wraps pods the way a Kubernetes list answers.
func podList(pods ...map[string]any) map[string]any {
	return map[string]any{"items": pods}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// body parses a provider config block the way the config loader leaves it.
func body(t *testing.T, src string) hcl.Body {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "vagabond.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing failed: %s", diags.Error())
	}

	return f.Body
}

// rawBlock parses a driver config the way the job parser leaves it.
func rawBlock(t *testing.T, src string) *job.RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing config failed: %s", diags.Error())
	}

	return &job.RawBlock{Body: f.Body}
}

// newID returns a fresh execution id.
func newID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
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
		Resources: &job.Resources{CPU: new(1000), Memory: new(2048)},
		Timeout:   new(job.Duration("15m")),
	}
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// A complete block and a key build a provider aimed at the region's hosts.
func TestNew_BuildsWithAKey(t *testing.T) {
	t.Parallel()

	p, diags := New(t.Context(), "ibm-code-engine", body(t, `
region     = "us-east"
project_id = "`+testProject+`"
`), []byte(testKey))
	if diags.HasErrors() {
		t.Fatalf("New() = %s", diags.Error())
	}

	if p.apiURL != "https://api.us-east.codeengine.cloud.ibm.com" {
		t.Errorf("apiURL = %q", p.apiURL)
	}

	if p.kubeURL != "https://proxy.us-east.codeengine.cloud.ibm.com" {
		t.Errorf("kubeURL = %q", p.kubeURL)
	}

	if p.Name() != "ibm-code-engine" {
		t.Errorf("Name() = %q", p.Name())
	}
}

// An empty key is an unusable credential, reported against the provider.
func TestNew_RejectsAnEmptyKey(t *testing.T) {
	t.Parallel()

	_, diags := New(t.Context(), "ibm-code-engine", body(t, `
region     = "us-east"
project_id = "`+testProject+`"
`), []byte("  \n"))
	if !diags.HasErrors() || !strings.Contains(diags.Error(), "Unusable credential") {
		t.Fatalf("New() = %v, want an unusable credential", diags)
	}
}

// A configuration error stops construction before the credential is looked at.
func TestNew_RejectsABadConfig(t *testing.T) {
	t.Parallel()

	_, diags := New(t.Context(), "ibm-code-engine", nil, []byte(testKey))
	if !diags.HasErrors() {
		t.Fatal("New() built a provider with no config block")
	}
}

// A key resolved again replaces the session, and the next token is exchanged
// with it.
func TestRecredential_ReplacesTheSession(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	before := p.auth.Load()

	if err := p.Recredential(t.Context(), []byte("rotated-key")); err != nil {
		t.Fatalf("Recredential() = %v", err)
	}

	if p.auth.Load() == before {
		t.Fatal("the session was not replaced")
	}

	f.on("GET "+runPath("vagabond-x"), reply(http.StatusOK, jobRunBody("vagabond-x", "running", "")))

	if _, err := p.getJobRun(t.Context(), "vagabond-x"); err != nil {
		t.Fatalf("getJobRun() = %v", err)
	}

	exchanges := f.requestsTo("POST /identity/token")
	if len(exchanges) == 0 || exchanges[len(exchanges)-1].form.Get("apikey") != "rotated-key" {
		t.Error("the new key was not used")
	}
}

// An empty key resolved again is refused and the old session kept.
func TestRecredential_RefusesAnEmptyKey(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)
	before := p.auth.Load()

	if err := p.Recredential(t.Context(), nil); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("Recredential() = %v, want ErrNoCredential", err)
	}

	if p.auth.Load() != before {
		t.Error("the session was replaced by an unusable one")
	}
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// Capabilities are constants: Code Engine's largest size and timeout, amd64
// containers, internet, any image.
func TestCapabilities(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)

	caps, err := p.Capabilities(t.Context())
	if err != nil {
		t.Fatalf("Capabilities() = %v", err)
	}

	if *caps.MaxResources.CPU != 12000 || *caps.MaxResources.Memory != 45776 {
		t.Errorf("MaxResources = %d millicores, %d MiB", *caps.MaxResources.CPU, *caps.MaxResources.Memory)
	}

	if caps.MaxDuration != 24*time.Hour {
		t.Errorf("MaxDuration = %s", caps.MaxDuration)
	}

	if len(caps.Drivers) != 1 || caps.Drivers[0] != job.DriverContainer ||
		len(caps.Architectures) != 1 || caps.Architectures[0] != job.ArchAMD64 {
		t.Errorf("drivers %v, architectures %v", caps.Drivers, caps.Architectures)
	}

	if !caps.InternetEgress || !caps.ArbitraryImages || caps.PrivateNetwork || caps.ObservedAt.IsZero() {
		t.Errorf("capabilities = %+v", caps)
	}
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// A submission is one job run named from the execution id, carrying the task
// inline and signed with the exchanged token.
func TestSubmit_CreatesTheJobRun(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	name := runName(id)

	f.on("POST /v2/projects/"+testProject+"/job_runs", reply(http.StatusAccepted, jobRunBody(name, "pending", "")))

	sub, err := p.Submit(t.Context(), id, containerTask(t))
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if sub.ProviderID != name || sub.State != execution.StateAccepted {
		t.Errorf("Submit() = %+v, want %s accepted", sub, name)
	}

	created := f.requestsTo("POST /v2/projects/" + testProject + "/job_runs")
	if len(created) != 1 {
		t.Fatalf("%d job runs created, want 1", len(created))
	}

	got := created[0]
	if got.body["name"] != name || got.body["image_reference"] != "golang:1.27" {
		t.Errorf("body = %v", got.body)
	}

	if got.query.Get("version") != apiVersion {
		t.Errorf("version = %q, want %q", got.query.Get("version"), apiVersion)
	}

	if got.header.Get("Authorization") != "Bearer tok" {
		t.Errorf("Authorization = %q", got.header.Get("Authorization"))
	}
}

// A task that cannot be translated is never sent.
func TestSubmit_RefusesAnUntranslatableTask(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	_, err := p.Submit(t.Context(), newID(t), &job.Task{
		Name:   "test",
		Driver: job.DriverContainer,
		Config: rawBlock(t, "command = \"true\"\n"),
	})
	if err == nil || plugin.Reroutable(err) {
		t.Fatalf("Submit() = %v, want an internal failure", err)
	}

	if n := len(f.requestsTo("POST /v2/projects/" + testProject + "/job_runs")); n != 0 {
		t.Errorf("%d job runs created, want none", n)
	}
}

// A name already taken is Vagabond's own mistake, so it is not rerouted.
func TestSubmit_DuplicateIsInternal(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("POST /v2/projects/"+testProject+"/job_runs",
		reply(http.StatusConflict, v2Error("resource_already_exists", "A resource with this name already exists.")))

	_, err := p.Submit(t.Context(), newID(t), containerTask(t))
	if err == nil || plugin.Reroutable(err) {
		t.Fatalf("Submit() = %v, want an internal failure", err)
	}
}

// -------------------------------------------------------------------------
// STATUS
// -------------------------------------------------------------------------

// Status carries the run's state and the run's own times; an end time appears
// only once the run reports it.
func TestStatus_ReadsTheRun(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	name := runName(id)

	f.on("GET "+runPath(name),
		reply(http.StatusOK, jobRunBody(name, "running", "")),
		reply(http.StatusOK, jobRunBody(name, "failed", "ContainerExitedCode3")))

	running, err := p.Status(t.Context(), id)
	if err != nil {
		t.Fatalf("Status() = %v", err)
	}

	if running.State != execution.StateRunning || running.StartedAt.IsZero() || !running.EndedAt.IsZero() {
		t.Errorf("running status = %+v", running)
	}

	failed, err := p.Status(t.Context(), id)
	if err != nil {
		t.Fatalf("Status() = %v", err)
	}

	want := time.Date(2026, 11, 10, 5, 58, 21, 0, time.UTC)
	if failed.State != execution.StateFailed || !failed.EndedAt.Equal(want) || !failed.UpdatedAt.Equal(want) {
		t.Errorf("failed status = %+v, want ended %s", failed, want)
	}

	if failed.ProviderID != name {
		t.Errorf("ProviderID = %q, want %q", failed.ProviderID, name)
	}
}

// A run Code Engine has no record of is an unknown execution.
func TestStatus_UnknownRun(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)

	_, err := p.Status(t.Context(), newID(t))
	if !errors.Is(err, plugin.ErrUnknownExecution) {
		t.Fatalf("Status() = %v, want ErrUnknownExecution", err)
	}
}

// A status the plugin does not know is an error, not a guess.
func TestStatus_UnknownStatus(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)

	f.on("GET "+runPath(runName(id)), reply(http.StatusOK, jobRunBody(runName(id), "suspended", "")))

	if _, err := p.Status(t.Context(), id); err == nil || plugin.Reroutable(err) {
		t.Fatalf("Status() = %v, want an internal failure", err)
	}
}

// -------------------------------------------------------------------------
// RESULT
// -------------------------------------------------------------------------

// The exit code comes from the pod, the duration from the instance, and the
// run's reason stands when the pod's says nothing more.
func TestResult_ReadsThePod(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	name := runName(id)
	podName := name + "-0-0"

	f.on("GET "+runPath(name), reply(http.StatusOK, jobRunBody(name, "failed", "OOMKilled")))
	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody(podName, time.Now(), new(137)))))

	result, err := p.Result(t.Context(), id)
	if err != nil {
		t.Fatalf("Result() = %v", err)
	}

	if result.ExitCode == nil || *result.ExitCode != 137 {
		t.Errorf("ExitCode = %v, want 137", result.ExitCode)
	}

	if result.Duration != 32*time.Second || result.Reason != "OOMKilled" {
		t.Errorf("Duration = %s, Reason = %q; want 32s, OOMKilled", result.Duration, result.Reason)
	}
}

// An evicted pod's reason and message are the result's reason, and its
// instance's 1970 finish gives way to the run's own times.
func TestResult_Evicted(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	name := runName(id)
	podName := name + "-0-0"

	const message = "Pod ephemeral local storage usage exceeds the total limit of containers 400M."

	run := jobRunBody(name, "failed", "ContainerExitedCode137")
	instance := run["status_details"].(map[string]any)["indices_details"].(map[string]any)["0"].(map[string]any)
	instance["finished_at"] = "1970-01-01T00:00:00Z"

	evicted := podBody(podName, time.Now(), new(137))
	evicted["status"].(map[string]any)["reason"] = "Evicted"
	evicted["status"].(map[string]any)["message"] = message

	f.on("GET "+runPath(name), reply(http.StatusOK, run))
	f.on("GET "+podsPath(), reply(http.StatusOK, podList(evicted)))

	result, err := p.Result(t.Context(), id)
	if err != nil {
		t.Fatalf("Result() = %v", err)
	}

	if result.ExitCode == nil || *result.ExitCode != 137 || result.Reason != "Evicted: "+message {
		t.Errorf("exit %v, Reason %q; want 137 and the eviction", result.ExitCode, result.Reason)
	}

	if result.Duration != 35*time.Second {
		t.Errorf("Duration = %s, want the run's 35s", result.Duration)
	}
}

// With the pod gone, the exit code is the one Code Engine recorded.
func TestResult_PodGone(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	name := runName(id)

	f.on("GET "+runPath(name), reply(http.StatusOK, jobRunBody(name, "failed", "ContainerExitedCode3")))
	f.on("GET "+podsPath(), reply(http.StatusOK, podList()))

	result, err := p.Result(t.Context(), id)
	if err != nil {
		t.Fatalf("Result() = %v", err)
	}

	if result.ExitCode == nil || *result.ExitCode != 3 {
		t.Errorf("ExitCode = %v, want 3", result.ExitCode)
	}
}

// A pod lookup that fails fails the result rather than guessing the exit code.
func TestResult_PodLookupFails(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	name := runName(id)

	f.on("GET "+runPath(name), reply(http.StatusOK, jobRunBody(name, "completed", "")))
	f.on("GET "+podsPath(), reply(http.StatusServiceUnavailable, map[string]any{"message": "unavailable"}))

	if _, err := p.Result(t.Context(), id); err == nil || !plugin.Reroutable(err) {
		t.Fatalf("Result() = %v, want an infrastructure failure", err)
	}
}

// A run that no longer exists has no result to give.
func TestResult_UnknownRun(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)

	if _, err := p.Result(t.Context(), newID(t)); !errors.Is(err, plugin.ErrUnknownExecution) {
		t.Fatalf("Result() = %v, want ErrUnknownExecution", err)
	}
}

// -------------------------------------------------------------------------
// CANCEL AND RELEASE
// -------------------------------------------------------------------------

// Cancel deletes the run, and a run already gone is not an error.
func TestCancel_DeletesTheRun(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)
	route := "DELETE " + runPath(runName(id))

	f.on(route, reply(http.StatusAccepted, map[string]any{}), reply(http.StatusNotFound, v2Error("job_run_not_found", "gone")))

	if err := p.Cancel(t.Context(), id); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}

	if err := p.Release(t.Context(), id); err != nil {
		t.Fatalf("Release() after Cancel = %v", err)
	}

	if n := len(f.requestsTo(route)); n != 2 {
		t.Errorf("%d deletes, want 2", n)
	}
}

// A delete Code Engine refuses is reported, not swallowed.
func TestCancel_ReportsARefusal(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	id := newID(t)

	f.on("DELETE "+runPath(runName(id)), reply(http.StatusInternalServerError, v2Error("internal", "boom")))

	if err := p.Cancel(t.Context(), id); err == nil {
		t.Fatal("Cancel() swallowed a 500")
	}
}
