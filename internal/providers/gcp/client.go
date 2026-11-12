// -------------------------------------------------------------------------------
// Cloud Run HTTP Client
//
// Author: Alex Freidah
//
// Authentication and JSON plumbing. Google's generated client is not used: the
// few calls this plugin makes do not justify the dependency, and oauth2 alone
// handles the part that is fiddly.
// -------------------------------------------------------------------------------

package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/afreidah/vagabond/internal/plugin"
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

// maxErrorBody is how much of a refusal is read for Google's message.
const maxErrorBody = 64 << 10

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
func (p *Provider) call(ctx context.Context, method, url string, payload, out any) error {
	resp, err := p.open(ctx, method, url, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	received, err := io.ReadAll(resp.Body)
	if err != nil {
		return plugin.Infrastructure(fmt.Errorf("reading response: %w", err))
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(received, out); err != nil {
		return plugin.Internal(fmt.Errorf("decoding response: %w", err))
	}

	return nil
}

// open sends one request and returns the response with its body unread, for
// a caller that streams it. Every non-2xx is classified, carrying Google's own
// message from the body.
func (p *Provider) open(ctx context.Context, method, url string, payload any) (*http.Response, error) {
	var body io.Reader

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, plugin.Internal(fmt.Errorf("encoding request: %w", err))
		}

		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, plugin.Internal(fmt.Errorf("building request: %w", err))
	}

	req.Header.Set("Accept", "application/json")

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.http.Load().Do(req)
	if err != nil {
		return nil, plugin.Infrastructure(fmt.Errorf("%s %s: %w", method, url, err))
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()

		received, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

		return nil, plugin.ClassifyHTTP(resp.StatusCode, plugin.RetryAfter(resp),
			fmt.Errorf("%s: %s", method, googleError(received)))
	}

	return resp, nil
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
