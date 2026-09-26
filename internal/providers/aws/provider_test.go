// -------------------------------------------------------------------------------
// Lambda Provider Tests
//
// Author: Alex Freidah
//
// Against an httptest server speaking Lambda's invoke API, through the real SDK
// client. What is under test is that AWS would have understood the request and
// that its answer is read correctly, which a mocked client cannot show.
// -------------------------------------------------------------------------------

package aws

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
)

// fakeLambda answers one invoke with whatever a test sets.
type fakeLambda struct {
	status        int
	errorType     string
	functionError string
	logs          string

	path    string
	headers http.Header
	event   event
	calls   int
}

func (f *fakeLambda) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	f.path = r.URL.Path
	f.headers = r.Header.Clone()

	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &f.event)

	if f.status >= 300 {
		w.Header().Set("X-Amzn-ErrorType", f.errorType)
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"message":"refused"}`))

		return
	}

	if f.functionError != "" {
		w.Header().Set("X-Amz-Function-Error", f.functionError)
	}

	w.Header().Set("X-Amz-Log-Result", base64.StdEncoding.EncodeToString([]byte(f.logs)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// newTestProvider points a provider's SDK client at f.
func newTestProvider(t *testing.T, f *fakeLambda) *Provider {
	t.Helper()

	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	client := lambda.New(lambda.Options{
		Region:       "us-east-1",
		BaseEndpoint: sdkaws.String(srv.URL),
		Credentials:  credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", ""),
		Retryer:      sdkaws.NopRetryer{},
		HTTPClient:   srv.Client(),
	})

	return &Provider{name: "aws-lambda", client: client}
}

// functionTask builds a task from a driver config fragment.
func functionTask(t *testing.T, config, env string) *job.Task {
	t.Helper()

	task := &job.Task{
		Name:   "test",
		Driver: job.DriverFunction,
		Config: rawBlock(t, config),
	}

	if env != "" {
		task.Env = rawBlock(t, env)
	}

	return task
}

func rawBlock(t *testing.T, src string) *job.RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "job.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing failed: %s", diags.Error())
	}

	return &job.RawBlock{Body: f.Body}
}

func newID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return id
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

func TestSubmit_InvokesSynchronouslyWithTheEvent(t *testing.T) {
	t.Parallel()

	f := &fakeLambda{logs: "hello\n" + reportLine}
	p := newTestProvider(t, f)
	id := newID(t)

	task := functionTask(t, `
function = "vagabond-runner"
args     = ["go", "test", "./..."]
`, `GOFLAGS = "-mod=mod"`)

	sub, err := p.Submit(t.Context(), id, task)
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if f.path != "/2015-03-31/functions/vagabond-runner/invocations" {
		t.Errorf("path = %q", f.path)
	}

	if got := f.headers.Get("X-Amz-Invocation-Type"); got != "RequestResponse" {
		t.Errorf("invocation type = %q, want RequestResponse", got)
	}

	if got := f.headers.Get("X-Amz-Log-Type"); got != "Tail" {
		t.Errorf("log type = %q, want Tail", got)
	}

	if f.event.ExecutionID != id.String() || len(f.event.Args) != 3 || f.event.Env["GOFLAGS"] != "-mod=mod" {
		t.Errorf("event = %+v", f.event)
	}

	if err := sub.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}

	if sub.State != execution.StateSucceeded || !sub.Result.Succeeded() {
		t.Errorf("state = %s, exit = %v; want a success", sub.State, sub.Result.ExitCode)
	}
}

// The task's metadata reaches the function as VAGABOND_META_* in the event's
// env.
func TestSubmit_EventCarriesMetadata(t *testing.T) {
	t.Parallel()

	f := &fakeLambda{logs: reportLine}
	p := newTestProvider(t, f)

	task := functionTask(t, `function = "f"`, "")
	task.Meta = map[string]string{"commit": "abc123"}

	if _, err := p.Submit(t.Context(), newID(t), task); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if got := f.event.Env["VAGABOND_META_COMMIT"]; got != "abc123" {
		t.Errorf("event env = %v, want VAGABOND_META_COMMIT", f.event.Env)
	}
}

// The REPORT line's bill supersedes the estimate, at the memory the function is
// configured with rather than what the task declared.
func TestSubmit_ReportsWhatAWSBilled(t *testing.T) {
	t.Parallel()

	p := newTestProvider(t, &fakeLambda{logs: "hello\n" + reportLine})

	sub, err := p.Submit(t.Context(), newID(t), functionTask(t, `function = "f"`, ""))
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	want := quota.Execution{Memory: 512, Duration: 13 * time.Millisecond}

	if sub.Result.Billed == nil || *sub.Result.Billed != want {
		t.Errorf("Billed = %+v, want %+v", sub.Result.Billed, want)
	}

	if sub.Result.Duration != 12340*time.Microsecond {
		t.Errorf("Duration = %v, want the reported 12.34ms", sub.Result.Duration)
	}

	if string(sub.Result.Logs) != "hello\n"+reportLine {
		t.Errorf("Logs = %q", sub.Result.Logs)
	}
}

// No REPORT line leaves the bill unset, so the ledger falls back to the
// estimate rather than charging nothing.
func TestSubmit_NoReportLeavesTheBillUnset(t *testing.T) {
	t.Parallel()

	p := newTestProvider(t, &fakeLambda{logs: "hello\n"})

	sub, err := p.Submit(t.Context(), newID(t), functionTask(t, `function = "f"`, ""))
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if sub.Result.Billed != nil {
		t.Errorf("Billed = %+v, want nil", sub.Result.Billed)
	}

	if sub.Result.Duration <= 0 {
		t.Error("Duration is unset; the ledger would price the run at nothing")
	}
}

// A handler that threw is the task failing: it ran and gave an answer.
func TestSubmit_FunctionErrorIsAFailedTask(t *testing.T) {
	t.Parallel()

	p := newTestProvider(t, &fakeLambda{functionError: "Unhandled", logs: reportLine})

	sub, err := p.Submit(t.Context(), newID(t), functionTask(t, `function = "f"`, ""))
	if err != nil {
		t.Fatalf("Submit() = %v, want a failed task rather than an error", err)
	}

	if sub.State != execution.StateFailed || sub.Result.Succeeded() {
		t.Errorf("state = %s, exit = %v; want a failure", sub.State, sub.Result.ExitCode)
	}

	if sub.Result.Billed == nil {
		t.Error("a failed run was not billed; AWS charges for it")
	}
}

func TestSubmit_TaskNamingNoFunction(t *testing.T) {
	t.Parallel()

	f := &fakeLambda{}
	p := newTestProvider(t, f)

	_, err := p.Submit(t.Context(), newID(t), functionTask(t, `args = ["x"]`, ""))

	var classified *plugin.Error
	if !errors.As(err, &classified) || classified.Class != plugin.ClassInternal {
		t.Errorf("Submit() = %v, want an internal failure", err)
	}

	if f.calls != 0 {
		t.Error("invoked with no function named")
	}
}

// -------------------------------------------------------------------------
// FAILURES
// -------------------------------------------------------------------------

func TestSubmit_ClassifiesRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		errorType     string
		wantClass     plugin.Class
		wantRetryable bool
	}{
		{
			name: "throttled", status: http.StatusTooManyRequests, errorType: "TooManyRequestsException",
			wantClass: plugin.ClassInfrastructure, wantRetryable: true,
		},
		{
			name: "service failure", status: http.StatusInternalServerError, errorType: "ServiceException",
			wantClass: plugin.ClassInfrastructure, wantRetryable: true,
		},
		{
			name: "no such function", status: http.StatusNotFound, errorType: "ResourceNotFoundException",
			wantClass: plugin.ClassInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := &fakeLambda{status: tt.status, errorType: tt.errorType}
			p := newTestProvider(t, f)

			_, err := p.Submit(t.Context(), newID(t), functionTask(t, `function = "f"`, ""))

			var classified *plugin.Error
			if !errors.As(err, &classified) {
				t.Fatalf("Submit() = %v, want a classified error", err)
			}

			if classified.Class != tt.wantClass || classified.Retryable != tt.wantRetryable {
				t.Errorf("class = %s retryable = %v, want %s %v",
					classified.Class, classified.Retryable, tt.wantClass, tt.wantRetryable)
			}

			// The SDK must not retry behind the ledger's back.
			if f.calls != 1 {
				t.Errorf("invoked %d times, want 1", f.calls)
			}
		})
	}
}

// Nothing outlives Submit, and the reaper reads that as the reservation
// standing.
func TestStatus_IsUnsupported(t *testing.T) {
	t.Parallel()

	p := newTestProvider(t, &fakeLambda{})

	if _, err := p.Status(t.Context(), newID(t)); !errors.Is(err, plugin.ErrUnsupported) {
		t.Errorf("Status() = %v, want ErrUnsupported", err)
	}
}
