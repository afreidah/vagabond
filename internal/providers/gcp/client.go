// -------------------------------------------------------------------------------
// Cloud Run HTTP Client
//
// Author: Alex Freidah
//
// Authentication and JSON plumbing. Google's generated client is deliberately
// not used: the handful of calls this plugin makes are simple enough that the
// generated surface would be a large dependency carried for very little, and
// oauth2 alone handles the part that is genuinely fiddly.
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
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/afreidah/vagabond/internal/plugin"
)

// The endpoints this plugin talks to, and the OAuth scope both accept.
//
// The scope is not a permission: what the credential may actually do is
// decided by the IAM roles it holds, which are run.developer and
// logging.viewer and nothing else.
const (
	runEndpoint     = "https://run.googleapis.com"
	loggingEndpoint = "https://logging.googleapis.com"

	scope = "https://www.googleapis.com/auth/cloud-platform"
)

// ErrNoCredential reports a Cloud Run provider configured without one.
var ErrNoCredential = errors.New("no credential supplied")

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// newHTTPClient builds a client that signs every request.
//
// JWTConfigFromJSON rather than the general credential loader, which is
// deprecated for accepting external-account configurations that can name an
// executable to run for a token. Vagabond's credential comes from wherever an
// operator's config pointed, so narrowing to service account keys means a
// malformed or substituted one is refused here rather than obeyed.
//
// The token source refreshes on its own, so nothing tracks expiry. A key that
// cannot be parsed fails now rather than on the first dispatch, which is the
// difference between an error at startup and a job that mysteriously will not
// run an hour later.
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
//
// Every non-2xx is classified, so the dispatcher can tell a quota rejection it
// should wait on from a malformed request that is Vagabond's own bug. Google's
// message is lifted out of the body because a bare status code sends someone
// reading the wrong page.
func (p *Provider) call(ctx context.Context, method, url string, payload, out any) error {
	var body io.Reader

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return plugin.Internal(fmt.Errorf("encoding request: %w", err))
		}

		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return plugin.Internal(fmt.Errorf("building request: %w", err))
	}

	req.Header.Set("Accept", "application/json")

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return plugin.Infrastructure(fmt.Errorf("%s %s: %w", method, url, err))
	}
	defer resp.Body.Close()

	received, err := io.ReadAll(resp.Body)
	if err != nil {
		return plugin.Infrastructure(fmt.Errorf("reading response: %w", err))
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		return plugin.ClassifyHTTP(resp.StatusCode, retryAfter(resp),
			fmt.Errorf("%s: %s", method, googleError(received)))
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(received, out); err != nil {
		return plugin.Internal(fmt.Errorf("decoding response: %w", err))
	}

	return nil
}

// retryAfter reads how long Google asked us to wait.
//
// Only sent with a rate limit, and only sometimes. Zero means the dispatcher
// picks its own backoff, which is the common case.
func retryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0
	}

	return time.Duration(seconds) * time.Second
}

// googleError renders whatever Google said went wrong.
//
// Their errors nest the useful sentence two levels down under several hundred
// bytes of help tokens and metadata, which hide it.
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
