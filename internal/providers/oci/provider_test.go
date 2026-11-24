// -------------------------------------------------------------------------------
// OCI Functions Provider Tests
//
// Author: Alex Freidah
//
// Against an httptest server playing both the Functions API and the invoke
// endpoint, through the real signing transport. The server checks each
// signature with the key's public half, so what is under test is that OCI
// would have accepted the request and that its answer is read correctly.
// -------------------------------------------------------------------------------

package oci

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/plugin/plugintest"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// The API key, function and invoke the tests name.
const (
	testTenancy     = "ocid1.tenancy.oc1..aaaatenancy"
	testUser        = "ocid1.user.oc1..aaaauser"
	testFingerprint = "12:34:56:78:9a:bc:de:f0:12:34:56:78:9a:bc:de:f0"
	testFunction    = "ocid1.fnfunc.oc1.iad.aaaafunction"
	testRequestID   = "B2A1F4E3/0C9D"
)

// testConfig is a provider block naming the test key, its fingerprint last.
const testConfig = `
region      = "us-ashburn-1"
tenancy     = "` + testTenancy + `"
user        = "` + testUser + `"
fingerprint = "` + testFingerprint + `"
`

// testKey is the private key every test signs with, generated once.
var testKey = sync.OnceValues(func() (*rsa.PrivateKey, error) { return rsa.GenerateKey(rand.Reader, 2048) })

// testKeyPEM is the test key as an operator supplies one.
func testKeyPEM(t *testing.T) []byte {
	t.Helper()

	key, err := testKey()
	if err != nil {
		t.Fatalf("generating the key: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// fakeOCI answers a function's lookup and its invoke with whatever a test
// sets: the reply okReply, unless a status refuses the call with an OCI code.
type fakeOCI struct {
	lookupStatus int
	status       int
	code         string
	reply        string

	url     string
	lookups int
	invokes int
	signed  error
	event   struct {
		ExecutionID string            `json:"execution_id"`
		Args        []string          `json:"args"`
		Env         map[string]string `json:"env"`
	}
}

// okReply is a function's reply to a task that succeeded.
const okReply = `{"exit_code":0,"output":"hello\n"}`

// ServeHTTP checks each request's signature, then answers a lookup with the
// function's configuration and an invoke with the reply or refusal set.
func (f *fakeOCI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.signed = errors.Join(f.signed, verify(r, raw))

	function := "/" + apiVersion + "/functions/" + testFunction

	switch {
	case r.Method == http.MethodGet && r.URL.Path == function:
		f.lookups++

		if f.lookupStatus != 0 {
			refuse(w, f.lookupStatus, "NotAuthorizedOrNotFound")

			return
		}

		_, _ = fmt.Fprintf(w, `{"id":%q,"invokeEndpoint":%q,"memoryInMBs":256,"shape":"GENERIC_X86"}`, testFunction, f.url)

	case r.Method == http.MethodPost && r.URL.Path == function+"/actions/invoke":
		f.invokes++
		_ = json.Unmarshal(raw, &f.event)

		w.Header().Set(requestIDHeader, testRequestID)

		if f.status != 0 {
			refuse(w, f.status, f.code)

			return
		}

		_, _ = w.Write([]byte(f.reply))

	default:
		http.NotFound(w, r)
	}
}

// refuse answers with status and OCI's error body for code.
func refuse(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"code":%q,"message":"refused"}`, code)
}

// signature reads the fields of an OCI Authorization header.
var signature = regexp.MustCompile(`(\w+)="([^"]*)"`)

// verify checks that r is signed by the test key over the headers OCI
// requires, and that its body is the one its hash names.
func verify(r *http.Request, body []byte) error {
	fields := map[string]string{}
	for _, m := range signature.FindAllStringSubmatch(r.Header.Get("Authorization"), -1) {
		fields[m[1]] = m[2]
	}

	if want := testTenancy + "/" + testUser + "/" + testFingerprint; fields["keyId"] != want {
		return fmt.Errorf("keyId = %q, want %q", fields["keyId"], want)
	}

	want := "date (request-target) host"
	if r.Method == http.MethodPost {
		want += " content-length content-type x-content-sha256"
	}

	if fields["headers"] != want {
		return fmt.Errorf("signed headers = %q, want %q", fields["headers"], want)
	}

	// The body is signed through its hash, so the hash must be of this body
	if r.Method == http.MethodPost {
		sum := sha256.Sum256(body)
		if r.Header.Get("X-Content-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
			return errors.New("x-content-sha256 is not the body's hash")
		}
	}

	if _, err := http.ParseTime(r.Header.Get("Date")); err != nil {
		return fmt.Errorf("date: %w", err)
	}

	lines := make([]string, 0, 6)
	for h := range strings.FieldsSeq(fields["headers"]) {
		switch h {
		case "(request-target)":
			lines = append(lines, h+": "+strings.ToLower(r.Method)+" "+r.URL.RequestURI())
		case "host":
			lines = append(lines, h+": "+r.Host)
		default:
			lines = append(lines, h+": "+r.Header.Get(h))
		}
	}

	sig, err := base64.StdEncoding.DecodeString(fields["signature"])
	if err != nil {
		return fmt.Errorf("decoding the signature: %w", err)
	}

	key, _ := testKey()
	hashed := sha256.Sum256([]byte(strings.Join(lines, "\n")))

	return rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hashed[:], sig)
}

// newTestProvider builds a provider with the test key and points it at f.
func newTestProvider(t *testing.T, f *fakeOCI) *Provider {
	t.Helper()

	if f.reply == "" {
		f.reply = okReply
	}

	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	f.url = srv.URL

	p, diags := New(t.Context(), "oci", plugintest.Body(t, testConfig), testKeyPEM(t))
	if diags.HasErrors() {
		t.Fatalf("New() failed: %s", diags.Error())
	}

	p.endpoint = srv.URL

	return p
}

// submit submits a task invoking the test function to p.
func submit(t *testing.T, p *Provider) (plugin.Submission, error) {
	t.Helper()

	return p.Submit(t.Context(), testkit.NewID(t), plugintest.FunctionTask(t, `function = "`+testFunction+`"`, ""))
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// Submit looks the function up, invokes it at its endpoint with the event, and
// both calls carry a signature OCI would accept. The task succeeds with the
// reply's output, billed at the function's memory.
func TestSubmit_LooksUpAndInvokesWithTheEvent(t *testing.T) {
	t.Parallel()

	f := &fakeOCI{}
	p := newTestProvider(t, f)
	id := testkit.NewID(t)

	task := plugintest.FunctionTask(t, "function = \""+testFunction+"\"\nargs = [\"--strict\"]", `CI = "true"`)

	sub, err := p.Submit(t.Context(), id, task)
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	if f.signed != nil || f.lookups != 1 || f.invokes != 1 {
		t.Errorf("signatures: %v; %d lookups and %d invokes, want 1 of each", f.signed, f.lookups, f.invokes)
	}

	if f.event.ExecutionID != id.String() || len(f.event.Args) != 1 || f.event.Env["CI"] != "true" {
		t.Errorf("event = %+v", f.event)
	}

	if err := sub.Validate(); err != nil || sub.State != execution.StateSucceeded || *sub.Result.ExitCode != 0 {
		t.Errorf("Submit() = %+v (%v), want a success", sub, err)
	}

	if sub.ProviderID != testRequestID || string(sub.Result.Logs) != "hello\n" {
		t.Errorf("provider id = %q, logs = %q; want the request ID and the reply's output", sub.ProviderID, sub.Result.Logs)
	}

	want := quota.Execution{Memory: 256, Duration: sub.Result.Duration}
	if sub.Result.Billed == nil || *sub.Result.Billed != want || want.Duration <= 0 {
		t.Errorf("Billed = %+v, want %+v", sub.Result.Billed, want)
	}
}

// The reply's exit code and output are the task's, and a failing run is still
// billed, since OCI charges for it.
func TestSubmit_ReplyDecidesTheTask(t *testing.T) {
	t.Parallel()

	sub, err := submit(t, newTestProvider(t, &fakeOCI{reply: `{"exit_code":3,"output":"3 issues\n"}`}))
	if err != nil {
		t.Fatalf("Submit() = %v, want a failed task rather than an error", err)
	}

	if sub.State != execution.StateFailed || *sub.Result.ExitCode != 3 || string(sub.Result.Logs) != "3 issues\n" {
		t.Errorf("state = %s, exit = %d, logs = %q; want exit 3 with the reply's output",
			sub.State, *sub.Result.ExitCode, sub.Result.Logs)
	}

	if sub.Result.Billed == nil {
		t.Error("a failed run was not billed")
	}
}

// A function that crashed, replied wrongly or too much, or ran past its
// timeout fails the task with exit code 1 and OCI's error as its output; it
// is not a provider failure to reroute.
func TestSubmit_FunctionFaultIsAFailedTask(t *testing.T) {
	t.Parallel()

	for _, code := range functionFaults {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			status := http.StatusBadGateway
			if code == "FunctionInvokeTimeout" {
				status = http.StatusGatewayTimeout
			}

			sub, err := submit(t, newTestProvider(t, &fakeOCI{status: status, code: code}))
			if err != nil {
				t.Fatalf("Submit() = %v, want a failed task rather than an error", err)
			}

			if sub.State != execution.StateFailed || *sub.Result.ExitCode != 1 || !strings.Contains(string(sub.Result.Logs), code) {
				t.Errorf("state = %s, exit = %d, logs = %q; want exit 1 with the error",
					sub.State, *sub.Result.ExitCode, sub.Result.Logs)
			}

			if sub.ProviderID != testRequestID || sub.Result.Billed == nil {
				t.Errorf("provider id = %q, billed = %v; want the request ID and a bill", sub.ProviderID, sub.Result.Billed)
			}
		})
	}
}

// A task naming no function fails as internal without calling OCI.
func TestSubmit_TaskNamingNoFunction(t *testing.T) {
	t.Parallel()

	f := &fakeOCI{}
	p := newTestProvider(t, f)

	_, err := p.Submit(t.Context(), testkit.NewID(t), plugintest.FunctionTask(t, `args = ["x"]`, ""))
	if failure, ok := errors.AsType[*plugin.Error](err); !ok || failure.Class != plugin.ClassInternal {
		t.Errorf("Submit() = %v, want an internal failure", err)
	}

	if f.lookups+f.invokes != 0 {
		t.Error("called OCI with no function named")
	}
}

// -------------------------------------------------------------------------
// FAILURES
// -------------------------------------------------------------------------

// Every other refusal is classified by its status with OCI's code and message,
// and a function that cannot be read is never invoked.
func TestSubmit_ClassifiesRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fake      *fakeOCI
		class     plugin.Class
		retryable bool
		is        error
		invokes   int
	}{
		{
			name:  "busy",
			fake:  &fakeOCI{status: http.StatusServiceUnavailable, code: "FunctionInvokeServiceUnavailable"},
			class: plugin.ClassInfrastructure, retryable: true, invokes: 1,
		},
		{
			name:  "rate limited",
			fake:  &fakeOCI{status: http.StatusTooManyRequests, code: "TooManyRequests"},
			class: plugin.ClassInfrastructure, retryable: true, invokes: 1,
		},
		{
			name:  "event too large",
			fake:  &fakeOCI{status: http.StatusRequestEntityTooLarge, code: "FunctionInvokeRequestContentTooLarge"},
			class: plugin.ClassInternal, invokes: 1,
		},
		{
			name:  "no such function",
			fake:  &fakeOCI{lookupStatus: http.StatusNotFound},
			class: plugin.ClassInternal, is: plugin.ErrNotFound,
		},
		{
			name:  "key refused",
			fake:  &fakeOCI{lookupStatus: http.StatusUnauthorized},
			class: plugin.ClassInternal, is: plugin.ErrUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := submit(t, newTestProvider(t, tt.fake))

			failure, ok := errors.AsType[*plugin.Error](err)
			if !ok || failure.Class != tt.class || failure.Retryable != tt.retryable {
				t.Fatalf("Submit() = %v, want %s retryable %v", err, tt.class, tt.retryable)
			}

			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("Submit() = %v, want %v", err, tt.is)
			}

			if !strings.Contains(err.Error(), ": refused") || tt.fake.invokes != tt.invokes {
				t.Errorf("Submit() = %v after %d invokes, want OCI's message after %d", err, tt.fake.invokes, tt.invokes)
			}
		})
	}
}

// failingSigner refuses to sign, as a body that cannot be read makes it.
type failingSigner struct{}

// Sign reports the failure.
func (failingSigner) Sign(*http.Request) error {
	return errors.New("unreadable body")
}

// A request that cannot be signed is never sent, and its body is closed as
// a transport must.
func TestSigner_RefusesWhatItCannotSign(t *testing.T) {
	t.Parallel()

	body := &closeRecorder{Reader: strings.NewReader("{}")}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://functions.example", body)

	resp, err := signer{sign: failingSigner{}, next: http.DefaultTransport}.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
	}

	if err == nil || !body.closed {
		t.Errorf("RoundTrip() = %v, body closed: %v; want an error and the body closed", err, body.closed)
	}
}

// closeRecorder is a request body reporting whether it was closed.
type closeRecorder struct {
	io.Reader

	closed bool
}

// Close records the close.
func (c *closeRecorder) Close() error {
	c.closed = true

	return nil
}

// A body that is not OCI's error is shown as received.
func TestOCIError_ShowsAnyOtherBody(t *testing.T) {
	t.Parallel()

	if got := ociError([]byte("<html>Bad Gateway</html>")); got != "<html>Bad Gateway</html>" {
		t.Errorf("ociError() = %q", got)
	}
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// Admission is offered the largest memory setting and the longest synchronous
// invoke, and nothing past either.
func TestCapabilities(t *testing.T) {
	t.Parallel()

	caps, err := newTestProvider(t, &fakeOCI{}).Capabilities(t.Context())
	if err != nil {
		t.Fatalf("Capabilities() = %v", err)
	}

	if !caps.WithinResources(plugin.Resources{CPU: 8000, Memory: 3072}) || caps.WithinResources(plugin.Resources{Memory: 3073}) {
		t.Error("memory limit is not 3072 MiB, or CPU is limited")
	}

	if !caps.WithinDuration(300*time.Second) || caps.WithinDuration(301*time.Second) {
		t.Error("duration limit is not 300 s")
	}
}
