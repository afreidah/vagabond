// -------------------------------------------------------------------------------
// Code Engine HTTP Client Tests
//
// Author: Alex Freidah
//
// The IAM exchange, how a failed call is classified, and how each of the three
// error shapes Code Engine's APIs send is read.
// -------------------------------------------------------------------------------

package ibm

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// AUTHENTICATION
// -------------------------------------------------------------------------

// The key is exchanged with the API-key grant and the token signs the call.
func TestSession_ExchangesTheKey(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET "+runPath("run1"), reply(http.StatusOK, jobRunBody("run1", "running", "")))

	if _, err := p.getJobRun(t.Context(), "run1"); err != nil {
		t.Fatalf("getJobRun() = %v", err)
	}

	exchanges := f.requestsTo("POST /identity/token")
	if len(exchanges) != 1 {
		t.Fatalf("%d exchanges, want 1", len(exchanges))
	}

	form := exchanges[0].form
	if form.Get("grant_type") != "urn:ibm:params:oauth:grant-type:apikey" || form.Get("apikey") != testKey {
		t.Errorf("form = %v", form)
	}

	if got := f.requestsTo("GET " + runPath("run1"))[0].header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
}

// A token is reused until it nears expiry, then exchanged again.
func TestSession_RefreshesEarly(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	// Inside tokenEarly, so every call needs a fresh token
	f.on("POST /identity/token", reply(http.StatusOK, map[string]any{"access_token": "short", "expires_in": 30}))
	f.on("GET "+runPath("run1"), reply(http.StatusOK, jobRunBody("run1", "running", "")))

	for range 2 {
		if _, err := p.getJobRun(t.Context(), "run1"); err != nil {
			t.Fatalf("getJobRun() = %v", err)
		}
	}

	if n := len(f.requestsTo("POST /identity/token")); n != 2 {
		t.Errorf("%d exchanges, want 2", n)
	}
}

// The delegated refresh token is asked for on Code Engine's behalf.
func TestSession_DelegatedToken(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	drt, err := p.auth.Load().iam.delegated(t.Context())
	if err != nil || drt != "drt" {
		t.Fatalf("delegated() = %q, %v", drt, err)
	}

	form := f.requestsTo("POST /identity/token")[0].form
	if form.Get("response_type") != "delegated_refresh_token" || form.Get("receiver_client_ids") != "ce" ||
		form.Get("delegated_refresh_token_expiry") != "3600" {
		t.Errorf("form = %v", form)
	}
}

// A delegated answer without the token is internal.
func TestSession_DelegatedTokenMissing(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("POST /identity/token", reply(http.StatusOK, map[string]any{}))

	if _, err := p.auth.Load().iam.delegated(t.Context()); err == nil || plugin.Reroutable(err) {
		t.Fatalf("delegated() = %v, want an internal failure", err)
	}
}

// IAM refusing the key, 400 for one it does not know, is unauthorized, so the
// credential is resolved again; and it reaches the caller through the client.
func TestSession_RefusedKeyIsUnauthorized(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden} {
		f, p := newFakeIBM(t)

		f.on("POST /identity/token", reply(status, map[string]any{
			"errorCode":    "BXNIM0415E",
			"errorMessage": "Provided API key could not be found.",
		}))

		_, err := p.getJobRun(t.Context(), "run1")
		if !errors.Is(err, plugin.ErrUnauthorized) || !strings.Contains(err.Error(), "BXNIM0415E") {
			t.Errorf("IAM %d: getJobRun() = %v, want unauthorized naming the code", status, err)
		}
	}
}

// IAM failing is infrastructure, like any other platform outage.
func TestSession_IAMOutageIsInfrastructure(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("POST /identity/token", reply(http.StatusServiceUnavailable, map[string]any{}))

	if _, err := p.getJobRun(t.Context(), "run1"); !plugin.Reroutable(err) {
		t.Fatalf("getJobRun() = %v, want an infrastructure failure", err)
	}
}

// A token answer without the token is internal.
func TestSession_TokenMissing(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("POST /identity/token", reply(http.StatusOK, map[string]any{"expires_in": 3600}))

	if _, err := p.getJobRun(t.Context(), "run1"); err == nil || plugin.Reroutable(err) {
		t.Fatalf("getJobRun() = %v, want an internal failure", err)
	}
}

// An empty key never makes a session.
func TestNewSession_RejectsAnEmptyKey(t *testing.T) {
	t.Parallel()

	if _, err := newSession(t.Context(), iamEndpoint, []byte(" \n")); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("newSession() = %v, want ErrNoCredential", err)
	}
}

// -------------------------------------------------------------------------
// CLASSIFICATION
// -------------------------------------------------------------------------

// Codes for declining work are infrastructure whatever status carries them,
// with the wait Code Engine asked for.
func TestClassify_DecliningCodes(t *testing.T) {
	t.Parallel()

	for _, code := range decliningCodes {
		f, p := newFakeIBM(t)

		f.on("GET "+runPath("run1"), func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "7")
			writeJSON(w, http.StatusBadRequest, v2Error(code, "slow down"))
		})

		_, err := p.getJobRun(t.Context(), "run1")

		classified, ok := errors.AsType[*plugin.Error](err)
		if !ok || !classified.Reroutable() || !classified.Retryable || classified.RetryAfter != 7*time.Second {
			t.Errorf("%s: getJobRun() = %v, want retryable infrastructure after 7s", code, err)
		}
	}
}

// A 403 names the region and project, since a project in another region is
// refused the same way as a missing permission.
func TestClassify_ForbiddenNamesTheRegion(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET "+runPath("run1"), reply(http.StatusForbidden, v2Error("forbidden", "This action is forbidden.")))

	_, err := p.getJobRun(t.Context(), "run1")
	if !errors.Is(err, plugin.ErrUnauthorized) {
		t.Fatalf("getJobRun() = %v, want unauthorized", err)
	}

	for _, want := range []string{"region us-east", "project " + testProject, "forbidden: This action is forbidden."} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
}

// No answer is read past maxResponse.
func TestFetch_BoundsTheBody(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET /big", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxResponse+1024))
	})

	got, err := p.fetch(t.Context(), http.MethodGet, p.apiURL+"/big", nil, nil)
	if err != nil {
		t.Fatalf("fetch() = %v", err)
	}

	if len(got) != maxResponse {
		t.Errorf("read %d bytes, want %d", len(got), maxResponse)
	}
}

// A body that cannot be decoded into what the caller expects is internal.
func TestCall_UndecodableAnswer(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET "+runPath("run1"), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})

	if _, err := p.getJobRun(t.Context(), "run1"); err == nil || plugin.Reroutable(err) {
		t.Fatalf("getJobRun() = %v, want an internal failure", err)
	}
}

// A server that cannot be reached is infrastructure.
func TestFetch_UnreachableIsInfrastructure(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)
	p.apiURL = "http://127.0.0.1:1"

	if _, err := p.getJobRun(t.Context(), "run1"); !plugin.Reroutable(err) {
		t.Fatalf("getJobRun() = %v, want an infrastructure failure", err)
	}
}

// -------------------------------------------------------------------------
// ERROR SHAPES
// -------------------------------------------------------------------------

// Each API's error shape yields its codes and message; anything else is shown
// as received, bounded.
func TestIBMError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		body    string
		codes   []string
		message string
	}{
		{
			"v2 list",
			`{"errors":[{"code":"field_invalid","message":"bad cpu"},{"code":"field_missing","message":"no image"}]}`,
			[]string{"field_invalid", "field_missing"},
			"field_invalid: bad cpu; field_missing: no image",
		},
		{
			"v1 single",
			`{"code":"forbidden","message":"This action is forbidden.","status_code":403}`,
			[]string{"forbidden"},
			"forbidden: This action is forbidden.",
		},
		{
			"kubernetes status",
			`{"kind":"Status","status":"Failure","message":"pods not found","reason":"NotFound","code":404}`,
			[]string{"NotFound"},
			"NotFound: pods not found",
		},
		{"message only", `{"message":"unavailable"}`, nil, "unavailable"},
		{"not json", "<html>bad gateway</html>", nil, "<html>bad gateway</html>"},
		{"json without a message", `{"other":1}`, nil, `{"other":1}`},
	}

	for _, tc := range cases {
		codes, message := ibmError([]byte(tc.body))

		if strings.Join(codes, ",") != strings.Join(tc.codes, ",") || message != tc.message {
			t.Errorf("%s: ibmError() = %v, %q; want %v, %q", tc.name, codes, message, tc.codes, tc.message)
		}
	}
}

// IAM's own shape is rendered as code and message.
func TestIAMError(t *testing.T) {
	t.Parallel()

	got := iamError([]byte(`{"errorCode":"BXNIM0415E","errorMessage":"Provided API key could not be found."}`))
	if got != "BXNIM0415E: Provided API key could not be found." {
		t.Errorf("iamError() = %q", got)
	}

	if got := iamError([]byte("down")); got != "down" {
		t.Errorf("iamError(plain) = %q", got)
	}
}

// An unrecognized body is cut at 500 characters.
func TestBounded(t *testing.T) {
	t.Parallel()

	got := bounded([]byte(strings.Repeat("x", 600)))
	if len(got) != 503 || !strings.HasSuffix(got, "...") {
		t.Errorf("bounded() is %d characters", len(got))
	}
}
