// -------------------------------------------------------------------------------
// Google API Client
//
// Author: Alex Freidah
//
// What the cloudrun and cloudbuild providers embed: authentication, the
// project they act in, and what a refusal from Google means. Requests go
// through the shared rest path. Google's generated clients are not used: the
// few calls these plugins make do not justify the dependency, and oauth2 alone
// handles the part that is fiddly.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/hcl/v2"
	"golang.org/x/oauth2"
	googleauth "golang.org/x/oauth2/google"

	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/rest"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Cloud Logging, where both providers read output, and the OAuth scope every
// Google API accepts. The scope is not a permission: the credential's IAM
// roles decide what it may do.
const (
	loggingEndpoint = "https://logging.googleapis.com"

	scope = "https://www.googleapis.com/auth/cloud-platform"
)

// ErrNoCredential reports a Google provider configured without one.
var ErrNoCredential = errors.New("no credential supplied")

// -------------------------------------------------------------------------
// THE SHARED BASE
// -------------------------------------------------------------------------

// API is what both providers embed: the client their requests are signed
// with, swapped whole when the key is resolved again, the configuration they
// act under, and the Cloud Logging endpoint their output is read from. LogsURL
// and Settle, how long a capture waits for late entries once the run is over,
// are fields so a test can point it at a local server and not wait.
type API struct {
	*plugin.Credentialed[http.Client]

	Config  *Config
	LogsURL string
	Settle  time.Duration

	name string
}

// NewAPI decodes a Google provider's config block and builds the client its
// requests are signed with, returning diagnostics so a bad field points at the
// line holding it.
func NewAPI(
	ctx context.Context, name, providerType string, body hcl.Body, credentials []byte,
) (*API, hcl.Diagnostics) {
	cfg, diags := decodeConfig(name, providerType, body)
	if diags.HasErrors() {
		return nil, diags
	}

	client, credDiags := plugin.NewCredentialed(ctx, name, credentials, newHTTPClient)
	if credDiags.HasErrors() {
		return nil, append(diags, credDiags...)
	}

	return &API{Credentialed: client, Config: cfg, LogsURL: loggingEndpoint, Settle: settleWait, name: name}, diags
}

// Name returns the routing identifier, the label of the provider block the
// API was built from.
func (a *API) Name() string {
	return a.name
}

// -------------------------------------------------------------------------
// AUTHENTICATION
// -------------------------------------------------------------------------

// newHTTPClient builds a client that signs every request, its token source
// refreshing on its own.
//
// JWTConfigFromJSON accepts only service account keys, unlike the general
// loader, whose external-account configurations can name an executable to run
// for a token. A key that cannot be parsed fails at startup, not at dispatch.
func newHTTPClient(ctx context.Context, credentials []byte) (*http.Client, error) {
	if len(credentials) == 0 {
		return nil, ErrNoCredential
	}

	cfg, err := googleauth.JWTConfigFromJSON(credentials, scope)
	if err != nil {
		return nil, fmt.Errorf("parsing the service account key: %w", err)
	}

	return oauth2.NewClient(ctx, cfg.TokenSource(ctx)), nil
}

// -------------------------------------------------------------------------
// REQUESTS
// -------------------------------------------------------------------------

// Call sends one request and decodes the response into out, which may be nil.
// Every failure is classified.
func (a *API) Call(ctx context.Context, method, url string, payload, out any) error {
	err := rest.Call(ctx, a.Client(), &rest.Request{Method: method, URL: url, Payload: payload}, out)

	return plugin.ClassifyResponse(err, refused)
}

// Open sends one request and returns the response with its body unread, for
// a caller that streams it. Every failure is classified.
func (a *API) Open(ctx context.Context, method, url string, payload any) (*http.Response, error) {
	resp, err := rest.Open(ctx, a.Client(), &rest.Request{Method: method, URL: url, Payload: payload})

	return resp, plugin.ClassifyResponse(err, refused)
}

// refused classifies a non-2xx by its status, carrying Google's own message
// from the body.
func refused(r *rest.Refusal) error {
	return plugin.ClassifyHTTP(r.Status, r.RetryAfter(), fmt.Errorf("%s: %s", r.Method, googleError(r.Body)))
}

// googleError renders whatever Google said went wrong, lifting the message out
// from under the help tokens and metadata that bury it.
func googleError(body []byte) string {
	var out struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if json.Unmarshal(body, &out) == nil && out.Error.Message != "" {
		return out.Error.Message
	}

	const limit = 500
	if len(body) > limit {
		return string(body[:limit]) + "..."
	}

	return string(body)
}
