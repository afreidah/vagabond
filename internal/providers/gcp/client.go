// -------------------------------------------------------------------------------
// Cloud Run HTTP Client
//
// Author: Alex Freidah
//
// Authentication, and what a refusal from Google means; requests go through
// the shared rest path. Google's generated client is not used: the few calls
// this plugin makes do not justify the dependency, and oauth2 alone handles
// the part that is fiddly.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/rest"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// The endpoints this plugin talks to, and the OAuth scope both accept. The
// scope is not a permission: the credential's IAM roles decide what it may do.
const (
	runEndpoint     = "https://run.googleapis.com"
	loggingEndpoint = "https://logging.googleapis.com"

	scope = "https://www.googleapis.com/auth/cloud-platform"
)

// ErrNoCredential reports a Cloud Run provider configured without one.
var ErrNoCredential = errors.New("no credential supplied")

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

	cfg, err := google.JWTConfigFromJSON(credentials, scope)
	if err != nil {
		return nil, fmt.Errorf("parsing the service account key: %w", err)
	}

	return oauth2.NewClient(ctx, cfg.TokenSource(ctx)), nil
}

// -------------------------------------------------------------------------
// REQUESTS
// -------------------------------------------------------------------------

// call sends one request and decodes the response into out, which may be nil.
// Every failure is classified.
func (p *Provider) call(ctx context.Context, method, url string, payload, out any) error {
	err := rest.Call(ctx, p.http.Load(), &rest.Request{Method: method, URL: url, Payload: payload}, out)

	return plugin.ClassifyResponse(err, refused)
}

// open sends one request and returns the response with its body unread, for
// a caller that streams it. Every failure is classified.
func (p *Provider) open(ctx context.Context, method, url string, payload any) (*http.Response, error) {
	resp, err := rest.Open(ctx, p.http.Load(), &rest.Request{Method: method, URL: url, Payload: payload})

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
