// -------------------------------------------------------------------------------
// API Authentication Tests
//
// Author: Alex Freidah
//
// With tokens configured: a request without a known token is refused, every
// route checks the access it declares in the namespace the request acts in,
// and a management route takes only a management token. Client certificates
// are covered by the API TLS tests.
// -------------------------------------------------------------------------------

package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/logging"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// The secrets of the tokens a tokenHarness accepts.
const (
	adminSecret     = "admin-0123456789abcdef"
	readerSecret    = "reader-0123456789abcdef"
	writerSecret    = "writer-0123456789abcdef"
	elsewhereSecret = "elsewhere-0123456789abcdef"
)

// tokenHarness is a server, logging to logger, that accepts a management
// token, a token that reads the default namespace, one that writes it, and
// one that writes only ci.
func tokenHarness(t *testing.T, logger *slog.Logger) *harness {
	t.Helper()

	return loggedHarness(t, logger, WithTokens(
		Token{Name: "admin", Secret: adminSecret, Management: true},
		Token{Name: "reader", Secret: readerSecret, Namespaces: map[string]Access{job.DefaultNamespace: Read}},
		Token{Name: "writer", Secret: writerSecret, Namespaces: map[string]Access{job.DefaultNamespace: Write}},
		Token{Name: "elsewhere", Secret: elsewhereSecret, Namespaces: map[string]Access{"ci": Write}},
	))
}

// request is one call to a route, with the body it needs to reach its
// namespace check.
type request struct {
	method, path string
	body         any
}

// -------------------------------------------------------------------------
// AUTHENTICATION
// -------------------------------------------------------------------------

// A request without a token, or with one the server does not know, is a 401
// that says which, and health answers without one. A bearer token in
// Authorization is taken as well as X-Vagabond-Token.
func TestAuth_Authenticate(t *testing.T) {
	h := tokenHarness(t, nil)

	var refused api.Error

	if status := h.call(t, http.MethodGet, "/v1/providers", nil, &refused); status != http.StatusUnauthorized ||
		refused.Error != "this server requires an API token" {
		t.Errorf("no token: %d %q, want 401 asking for one", status, refused.Error)
	}

	if status := h.as("not-a-token-the-server-has").call(t, http.MethodGet, "/v1/providers", nil, &refused); status != http.StatusUnauthorized ||
		refused.Error != "this server does not accept the API token sent" {
		t.Errorf("unknown token: %d %q, want 401 refusing it", status, refused.Error)
	}

	if status := h.call(t, http.MethodGet, healthPath, nil, nil); status != http.StatusOK {
		t.Errorf("health without a token = %d, want 200", status)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.url+"/v1/providers", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+readerSecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET providers: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("bearer token = %d, want 200", resp.StatusCode)
	}
}

// The 401 names the scheme a client should answer with.
func TestAuth_ChallengesForABearerToken(t *testing.T) {
	h := tokenHarness(t, nil)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.url+"/v1/nodes", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET nodes: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
}

// -------------------------------------------------------------------------
// ACCESS
// -------------------------------------------------------------------------

// A token that writes only another namespace is refused by every route that
// acts in one, before anything is read or changed, and by every management
// route; it may still list nodes and providers.
func TestAuth_EveryRouteChecksItsNamespace(t *testing.T) {
	h := tokenHarness(t, nil)
	run, _ := ended(t, h.executions, time.Now(), 0)
	dispatch := dispatched(t, h.executions).ID.String()
	exec := run.String()

	refused := []request{
		{http.MethodPost, "/v1/jobs", api.RegisterRequest{Source: helloJob}},
		{http.MethodGet, "/v1/jobs", nil},
		{http.MethodPost, "/v1/jobs/run", api.RunRequest{Source: sleepyJob}},
		{http.MethodPost, "/v1/jobs/plan", api.PlanRequest{Source: sleepyJob}},
		{http.MethodPost, "/v1/jobs/plan", api.PlanRequest{Name: "hello"}},
		{http.MethodGet, "/v1/job/hello", nil},
		{http.MethodDelete, "/v1/job/hello", nil},
		{http.MethodPost, "/v1/job/hello/dispatch", api.DispatchRequest{}},
		{http.MethodGet, "/v1/dispatch/" + dispatch, nil},
		{http.MethodGet, "/v1/executions", nil},
		{http.MethodGet, "/v1/execution/" + exec, nil},
		{http.MethodGet, "/v1/execution/" + exec + "/logs", nil},
		{http.MethodDelete, "/v1/execution/" + exec, nil},
		{http.MethodPost, "/v1/provider/fn/disable", nil},
		{http.MethodPost, "/v1/provider/fn/enable", nil},
	}

	elsewhere := h.as(elsewhereSecret)

	for _, r := range refused {
		if status := elsewhere.call(t, r.method, r.path, r.body, nil); status != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", r.method, r.path, status)
		}
	}

	for _, path := range []string{"/v1/nodes", "/v1/providers"} {
		if status := elsewhere.call(t, http.MethodGet, path, nil, nil); status != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 for any token", path, status)
		}
	}
}

// Read lets a token look but not change anything, write lets it do both, and
// a management token may do either in any namespace. The refusal names the
// token, the access and the namespace.
func TestAuth_Policies(t *testing.T) {
	h := tokenHarness(t, nil)
	run, _ := ended(t, h.executions, time.Now(), 0)
	dispatch := "/v1/dispatch/" + dispatched(t, h.executions).ID.String()
	exec := "/v1/execution/" + run.String()

	reader, writer, admin := h.as(readerSecret), h.as(writerSecret), h.as(adminSecret)

	for _, sender := range []*harness{reader, writer, admin} {
		for _, path := range []string{exec, exec + "/logs", dispatch, "/v1/executions"} {
			if status := sender.call(t, http.MethodGet, path, nil, nil); status != http.StatusOK {
				t.Errorf("token %s: GET %s = %d, want 200", sender.token, path, status)
			}
		}
	}

	var refused api.Error

	if status := reader.call(t, http.MethodDelete, exec, nil, &refused); status != http.StatusForbidden ||
		refused.Error != `token "reader" has no write access to namespace "default"` {
		t.Errorf("reader cancelling: %d %q, want 403 naming the access missing", status, refused.Error)
	}

	for _, sender := range []*harness{writer, admin} {
		if status := sender.call(t, http.MethodDelete, exec, nil, nil); status != http.StatusOK {
			t.Errorf("token %s cancelling = %d, want 200", sender.token, status)
		}
	}
}

// A namespace check on a request that never authenticated refuses it, so a
// route registered without its access fails closed.
func TestAuth_PermitWithoutAuthenticating(t *testing.T) {
	s := &Server{tokens: make([]token, 1)}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/jobs", nil)

	if err := s.permit(r, job.DefaultNamespace); err == nil || err.Error() != "the request was not authenticated" {
		t.Errorf("permit() = %v, want the request refused", err)
	}
}

// Only a management token turns a provider off and on. The provider records
// the token's name as who disabled it, and the server's log names it too.
func TestAuth_Management(t *testing.T) {
	var out testkit.Buffer

	h := tokenHarness(t, logging.New(&out, logging.Options{JSON: true}))

	var refused api.Error

	if status := h.as(writerSecret).call(t, http.MethodPost, "/v1/provider/fn/disable", nil, &refused); status != http.StatusForbidden ||
		refused.Error != `token "writer" is not a management token` {
		t.Errorf("writer disabling: %d %q, want 403", status, refused.Error)
	}

	var disabled api.ProviderListStub

	if status := h.as(adminSecret).call(t, http.MethodPost, "/v1/provider/fn/disable", nil, &disabled); status != http.StatusOK ||
		disabled.Disabled == nil || disabled.Disabled.By != "admin" {
		t.Errorf("admin disabling: %d %+v, want the provider disabled by admin", status, disabled)
	}

	for line := range strings.Lines(out.String()) {
		var fields map[string]any
		if json.Unmarshal([]byte(line), &fields) == nil && fields["msg"] == "provider disabled" {
			if fields["token"] != "admin" || fields["by"] != "admin" {
				t.Errorf("provider disabled line = %v, want the admin token named", fields)
			}

			return
		}
	}

	t.Errorf("no provider disabled line in:\n%s", out.String())
}
