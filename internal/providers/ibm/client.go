// -------------------------------------------------------------------------------
// Code Engine HTTP Client
//
// Author: Alex Freidah
//
// Authentication and JSON plumbing for both of Code Engine's APIs: the regional
// v2 API and the project's Kubernetes API, which take the same IAM token. IBM's
// generated SDK is not used: oauth2 handles the token's refresh, and the rest is
// a handful of calls.
// -------------------------------------------------------------------------------

package ibm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// iamEndpoint exchanges an API key for an access token, or for the delegated
// refresh token the kubeconfig call takes.
const iamEndpoint = "https://iam.cloud.ibm.com/identity/token"

// mediaJSON is the media type every v2 and IAM call accepts, and the v2 calls
// send.
const mediaJSON = "application/json"

// How early a token is replaced, how long an exchange may take, and how much of
// any response is read.
const (
	tokenEarly  = time.Minute
	iamTimeout  = 30 * time.Second
	maxResponse = 4 << 20
)

// decliningCodes are the Code Engine error codes for declining work rather
// than refusing it, infrastructure whatever status carries them.
var decliningCodes = []string{"quota_exceeded", "requests_too_many"}

// ErrNoCredential reports a Code Engine provider configured without an API key.
var ErrNoCredential = errors.New("no credential supplied")

// -------------------------------------------------------------------------
// AUTHENTICATION
// -------------------------------------------------------------------------

// session signs requests: the client, and the IAM source behind it for the one
// call that needs a delegated token. Recredential swaps it whole.
type session struct {
	http *http.Client
	iam  *iamSource
}

// newSession builds a session from an API key. The key is opaque, so a bad one
// fails at its first use, as unauthorized, rather than at startup.
func newSession(ctx context.Context, iamURL string, credential []byte) (*session, error) {
	key := strings.TrimSpace(string(credential))
	if key == "" {
		return nil, ErrNoCredential
	}

	src := &iamSource{url: iamURL, key: key, http: &http.Client{Timeout: iamTimeout}}
	tokens := oauth2.ReuseTokenSourceWithExpiry(nil, src, tokenEarly)

	return &session{http: oauth2.NewClient(ctx, tokens), iam: src}, nil
}

// iamSource exchanges the API key for tokens. It holds the key, which leaves
// this type only in the body of a request to IAM.
type iamSource struct {
	url  string
	key  string
	http *http.Client
}

// Token swaps the key for an access token. oauth2 passes no context, so the
// client's own timeout bounds the exchange.
func (s *iamSource) Token() (*oauth2.Token, error) {
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}

	if err := s.exchange(context.Background(), nil, &out); err != nil {
		return nil, err
	}

	if out.AccessToken == "" {
		return nil, plugin.Internal(errors.New("iam: the token response carries no access token"))
	}

	return &oauth2.Token{
		AccessToken: out.AccessToken,
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}, nil
}

// delegated swaps the key for the refresh token Code Engine takes in place of a
// login, which only the kubeconfig call needs.
func (s *iamSource) delegated(ctx context.Context) (string, error) {
	var out struct {
		Token string `json:"delegated_refresh_token"`
	}

	err := s.exchange(ctx, url.Values{
		"response_type":                  {"delegated_refresh_token"},
		"receiver_client_ids":            {"ce"},
		"delegated_refresh_token_expiry": {"3600"},
	}, &out)
	if err != nil {
		return "", err
	}

	if out.Token == "" {
		return "", plugin.Internal(errors.New("iam: the response carries no delegated refresh token"))
	}

	return out.Token, nil
}

// exchange posts the key to IAM with any extra form fields and decodes the
// answer into out.
func (s *iamSource) exchange(ctx context.Context, extra url.Values, out any) error {
	form := url.Values{
		"grant_type": {"urn:ibm:params:oauth:grant-type:apikey"},
		"apikey":     {s.key},
	}
	maps.Copy(form, extra)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(form.Encode()))
	if err != nil {
		return plugin.Internal(fmt.Errorf("iam: building request: %w", err))
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", mediaJSON)

	resp, err := s.http.Do(req)
	if err != nil {
		return plugin.Infrastructure(fmt.Errorf("iam: %w", err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return plugin.Infrastructure(fmt.Errorf("iam: reading response: %w", err))
	}

	if resp.StatusCode != http.StatusOK {
		return iamRefusal(resp, body)
	}

	if err := json.Unmarshal(body, out); err != nil {
		return plugin.Internal(fmt.Errorf("iam: decoding response: %w", err))
	}

	return nil
}

// -------------------------------------------------------------------------
// REQUESTS
// -------------------------------------------------------------------------

// call sends one request and decodes the JSON answer into out, which may be
// nil.
func (p *Provider) call(ctx context.Context, method, target string, payload, out any) error {
	body, err := p.fetch(ctx, method, target, nil, payload)
	if err != nil || out == nil {
		return err
	}

	if err := json.Unmarshal(body, out); err != nil {
		return plugin.Internal(fmt.Errorf("decoding response: %w", err))
	}

	return nil
}

// fetch sends one request and returns the raw answer, up to maxResponse. A
// token refusal arrives already classified and passes through as it is.
func (p *Provider) fetch(
	ctx context.Context, method, target string, header http.Header, payload any,
) ([]byte, error) {
	var body io.Reader

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, plugin.Internal(fmt.Errorf("encoding request: %w", err))
		}

		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, plugin.Internal(fmt.Errorf("building request: %w", err))
	}

	maps.Copy(req.Header, header)

	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", mediaJSON)
	}

	if payload != nil {
		req.Header.Set("Content-Type", mediaJSON)
	}

	resp, err := p.auth.Load().http.Do(req)
	if err != nil {
		if classified, ok := errors.AsType[*plugin.Error](err); ok {
			return nil, classified
		}

		return nil, plugin.Infrastructure(fmt.Errorf("%s %s: %w", method, req.URL.Path, err))
	}
	defer resp.Body.Close()

	received, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, plugin.Infrastructure(fmt.Errorf("reading response: %w", err))
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, p.classify(resp, method, received)
	}

	return received, nil
}

// classify turns a non-2xx into a classified failure. Declining codes are
// infrastructure whatever status carries them, and a 403 names the region and
// project, since a project outside the region is refused the same way.
func (p *Provider) classify(resp *http.Response, method string, body []byte) error {
	codes, message := ibmError(body)
	cause := fmt.Errorf("%s %s: %s", method, resp.Request.URL.Path, message)

	declining := slices.ContainsFunc(codes, func(code string) bool {
		return slices.Contains(decliningCodes, code)
	})
	if declining {
		e := plugin.Infrastructure(fmt.Errorf("http %d: %w", resp.StatusCode, cause))
		e.RetryAfter = plugin.RetryAfter(resp)

		return e
	}

	if resp.StatusCode == http.StatusForbidden {
		cause = fmt.Errorf("%w (region %s, project %s; a project in another region is refused the same way)",
			cause, p.cfg.Region, p.cfg.ProjectID)
	}

	return plugin.ClassifyHTTP(resp.StatusCode, plugin.RetryAfter(resp), cause)
}

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// iamRefusal classifies a non-200 from IAM. IAM answers 400 for a key it does
// not know, so 400, 401 and 403 are all unauthorized and the credential is
// resolved again.
func iamRefusal(resp *http.Response, body []byte) error {
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return plugin.Internal(fmt.Errorf("%w: IAM refused the API key: %s",
			plugin.ErrUnauthorized, iamError(body)))
	default:
		return plugin.ClassifyHTTP(resp.StatusCode, plugin.RetryAfter(resp),
			fmt.Errorf("iam: %s", iamError(body)))
	}
}

// ibmError lifts the codes and message out of whichever shape the answer
// takes: the v2 API's error list, the v1 API's single error, or a Kubernetes
// Status. Anything else is shown as received, bounded.
func ibmError(body []byte) (codes []string, message string) {
	var out struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Reason  string          `json:"reason"`
	}

	if json.Unmarshal(body, &out) != nil {
		return nil, bounded(body)
	}

	if len(out.Errors) > 0 {
		messages := make([]string, 0, len(out.Errors))
		for _, e := range out.Errors {
			codes = append(codes, e.Code)
			messages = append(messages, e.Code+": "+e.Message)
		}

		return codes, strings.Join(messages, "; ")
	}

	if out.Message == "" {
		return nil, bounded(body)
	}

	// v1 carries its code as a string; a Kubernetes Status carries a number and a reason
	var code string
	if json.Unmarshal(out.Code, &code) != nil {
		code = out.Reason
	}

	if code == "" {
		return nil, out.Message
	}

	return []string{code}, code + ": " + out.Message
}

// iamError renders IAM's own code and message, which never repeat the key.
func iamError(body []byte) string {
	var out struct {
		Code    string `json:"errorCode"`
		Message string `json:"errorMessage"`
	}

	if json.Unmarshal(body, &out) == nil && out.Message != "" {
		return out.Code + ": " + out.Message
	}

	return bounded(body)
}

// bounded shows an unrecognized body, cut short so a page of HTML cannot flood
// the error.
func bounded(body []byte) string {
	const limit = 500

	s := strings.TrimSpace(string(body))
	if len(s) > limit {
		return s[:limit] + "..."
	}

	return s
}
