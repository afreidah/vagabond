// -------------------------------------------------------------------------------
// Google Provider Test Kit
//
// Author: Alex Freidah
//
// What the Cloud Run and Cloud Build tests share: a fake Google answering
// canned responses by route, the shared base pointed at it, a key and config
// block New accepts, and a real project's settings for the live tests.
// -------------------------------------------------------------------------------

package gcptest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/plugin/plugintest"
	"github.com/afreidah/vagabond/internal/providers/gcp"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// Key is shaped like a service account key file. The private key is only read
// when a token is fetched, which no unit test does.
//
//nolint:gosec // shaped like a key, holds no secret
const Key = `{
  "type": "service_account",
  "client_email": "dispatcher@test-project.iam.gserviceaccount.com",
  "private_key": "unused"
}`

// Config is a complete config block for the test project.
const Config = `
project                 = "test-project"
region                  = "us-central1"
runtime_service_account = "runtime@test-project.iam.gserviceaccount.com"
`

// -------------------------------------------------------------------------
// FAKE GOOGLE
// -------------------------------------------------------------------------

// Request is one request the fake Google saw.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

// Google stands in for Cloud Run, Cloud Build and Cloud Logging. Handlers are
// keyed by "METHOD /path", a trailing star meaning prefix; anything unmatched
// 404s. URL is where it listens.
type Google struct {
	URL      string
	Requests []Request

	t         *testing.T
	handlers  map[string]any
	sequences map[string][]any
}

// NewGoogle starts a fake Google, stopped when the test ends, and the shared
// base of a provider named name with every request sent to it unsigned and no
// wait for late entries.
func NewGoogle(t *testing.T, name string) (*Google, *gcp.API) {
	t.Helper()

	g := &Google{t: t, handlers: map[string]any{}, sequences: map[string][]any{}}

	server := httptest.NewServer(g)
	t.Cleanup(server.Close)

	g.URL = server.URL

	api, diags := gcp.NewAPI(t.Context(), name, "test", plugintest.Body(t, Config), []byte(Key))
	if diags.HasErrors() {
		t.Fatalf("NewAPI() = %s", diags.Error())
	}

	// Unsigned, since the fake needs no auth
	api.Credentialed, diags = plugin.NewCredentialed(t.Context(), name, nil,
		func(context.Context, []byte) (*http.Client, error) { return server.Client(), nil })
	if diags.HasErrors() {
		t.Fatalf("NewCredentialed() = %s", diags.Error())
	}

	api.LogsURL, api.Settle = server.URL, 0

	return g, api
}

// Respond registers what the fake returns for one route, every time it is
// asked.
func (g *Google) Respond(route string, body any) {
	g.handlers[route] = body
}

// RespondInTurn registers successive responses for one route, for the APIs
// whose answer depends on how many times they have been asked.
func (g *Google) RespondInTurn(route string, bodies ...any) {
	g.sequences[route] = bodies
}

// Request returns the nth request the fake saw, failing the test when fewer
// were made.
func (g *Google) Request(n int) Request {
	g.t.Helper()

	if n >= len(g.Requests) {
		g.t.Fatalf("wanted request %d, only %d were made", n, len(g.Requests))
	}

	return g.Requests[n]
}

// ServeHTTP records the request and answers from the queued, exact, then
// prefix routes, in that order.
func (g *Google) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	g.Requests = append(g.Requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body})

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
		if strings.HasSuffix(route, "*") && strings.HasPrefix(key, strings.TrimSuffix(route, "*")) {
			writeJSON(w, http.StatusOK, response)

			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{"message": "no handler for " + key},
	})
}

// writeJSON writes body as a JSON response with the given status, as Google
// answers every call.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// -------------------------------------------------------------------------
// LIVE
// -------------------------------------------------------------------------

// Live waits generously on a container that does almost nothing, because a
// cold start and image pull have taken two minutes for a four second task.
var Live = plugintest.Live{Poll: 10 * time.Second, Deadline: 6 * time.Minute}

// LiveConfig reads a real project's config block and key from the
// environment, or skips.
func LiveConfig(t *testing.T) (hcl.Body, []byte) {
	t.Helper()

	path := os.Getenv("VAGABOND_GCP_CREDENTIALS")
	if path == "" {
		t.Skip("VAGABOND_GCP_CREDENTIALS is unset")
	}

	credentials, err := os.ReadFile(path) //nolint:gosec // the operator named this file
	if err != nil {
		t.Fatalf("reading the credential: %v", err)
	}

	project := os.Getenv("VAGABOND_GCP_PROJECT")
	runtimeSA := os.Getenv("VAGABOND_GCP_RUNTIME_SA")

	if project == "" || runtimeSA == "" {
		t.Fatal("VAGABOND_GCP_PROJECT and VAGABOND_GCP_RUNTIME_SA are required")
	}

	region := os.Getenv("VAGABOND_GCP_REGION")
	if region == "" {
		region = "us-central1"
	}

	return plugintest.Body(t, `
project                 = "`+project+`"
region                  = "`+region+`"
runtime_service_account = "`+runtimeSA+`"
`), credentials
}
