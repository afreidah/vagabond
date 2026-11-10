// -------------------------------------------------------------------------------
// API Client
//
// Author: Alex Freidah
//
// One method per /v1 route, taking and returning the types in this package. A
// non-2xx answer comes back as a *ResponseError carrying the status and the
// server's api.Error body, diagnostics included.
// -------------------------------------------------------------------------------

package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hashicorp/go-cleanhttp"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// DefaultAddress is where a client looks for a server when told nothing else,
// matching the server's default bind.
const DefaultAddress = "http://127.0.0.1:4747"

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Client talks to one server.
type Client struct {
	base *url.URL
	http *http.Client
}

// ResponseError is a non-2xx answer: its status and the server's body.
type ResponseError struct {
	Status int
	Body   Error
}

// Error returns the server's message.
func (e *ResponseError) Error() string {
	return e.Body.Error
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// NewClient returns a client for the server at address, on a pooled transport
// of its own rather than the process-wide default. An address without a scheme
// is taken as http, and an empty one is DefaultAddress. A tlsConfig needs an
// https address.
func NewClient(address string, tlsConfig *tls.Config) (*Client, error) {
	if address == "" {
		address = DefaultAddress
	}

	if !strings.Contains(address, "://") {
		address = "http://" + address
	}

	base, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("server address %q: %w", address, err)
	}

	if tlsConfig != nil && base.Scheme != "https" {
		return nil, fmt.Errorf("server address %q: TLS options need an https:// address", address)
	}

	transport := cleanhttp.DefaultPooledTransport()
	transport.TLSClientConfig = tlsConfig

	return &Client{base: base, http: &http.Client{Transport: transport}}, nil
}

// -------------------------------------------------------------------------
// JOBS
// -------------------------------------------------------------------------

// Register stores a job file as a new version when it changed.
func (c *Client) Register(ctx context.Context, namespace, source string) (*RegisterResponse, error) {
	var out RegisterResponse

	return &out, c.do(ctx, http.MethodPost, "/v1/jobs", namespace, RegisterRequest{Source: source}, &out)
}

// Jobs lists every job registered in namespace, stopped ones included.
func (c *Client) Jobs(ctx context.Context, namespace string) ([]Job, error) {
	var out []Job

	if err := c.do(ctx, http.MethodGet, "/v1/jobs", namespace, nil, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// JobStatus reads a registered job with its versions and recent executions.
func (c *Client) JobStatus(ctx context.Context, namespace, name string) (*JobStatus, error) {
	var out JobStatus

	return &out, c.do(ctx, http.MethodGet, "/v1/job/"+url.PathEscape(name), namespace, nil, &out)
}

// StopJob deregisters a job so it can no longer be dispatched.
func (c *Client) StopJob(ctx context.Context, namespace, name string) (*Job, error) {
	var out Job

	return &out, c.do(ctx, http.MethodDelete, "/v1/job/"+url.PathEscape(name), namespace, nil, &out)
}

// -------------------------------------------------------------------------
// RUNS
// -------------------------------------------------------------------------

// Dispatch starts a run of a registered job's current version.
func (c *Client) Dispatch(
	ctx context.Context, namespace, name string, meta map[string]string,
) (*DispatchResponse, error) {
	var out DispatchResponse

	path := "/v1/job/" + url.PathEscape(name) + "/dispatch"

	return &out, c.do(ctx, http.MethodPost, path, namespace, DispatchRequest{Meta: meta}, &out)
}

// Run starts a run of a job file without registering it.
func (c *Client) Run(
	ctx context.Context, namespace, source string, meta map[string]string,
) (*DispatchResponse, error) {
	var out DispatchResponse

	return &out, c.do(ctx, http.MethodPost, "/v1/jobs/run", namespace, RunRequest{Source: source, Meta: meta}, &out)
}

// Plan shows where each task of a job file or registered job would run.
func (c *Client) Plan(ctx context.Context, namespace string, req PlanRequest) (*Plan, error) {
	var out Plan

	return &out, c.do(ctx, http.MethodPost, "/v1/jobs/plan", namespace, req, &out)
}

// DispatchStatus reads a run and the executions it has created so far.
func (c *Client) DispatchStatus(ctx context.Context, id string) (*Dispatch, error) {
	var out Dispatch

	return &out, c.do(ctx, http.MethodGet, "/v1/dispatch/"+url.PathEscape(id), "", nil, &out)
}

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// Execution reads one attempt.
func (c *Client) Execution(ctx context.Context, id string) (*Execution, error) {
	var out Execution

	return &out, c.do(ctx, http.MethodGet, "/v1/execution/"+url.PathEscape(id), "", nil, &out)
}

// Logs reads an execution's stored output from byte offset on.
func (c *Client) Logs(ctx context.Context, id string, offset int64) ([]byte, error) {
	resp, err := c.openLogs(ctx, id, offset, false)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	logs, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading logs of %s: %w", id, err)
	}

	return logs, nil
}

// FollowLogs copies a running execution's output from byte offset on to w as
// it is written, until the server ends the stream. An error means the stream
// broke rather than ended.
func (c *Client) FollowLogs(ctx context.Context, id string, offset int64, w io.Writer) error {
	resp, err := c.openLogs(ctx, id, offset, true)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("following the output of %s: %w", id, err)
	}

	return nil
}

// openLogs requests an execution's output from byte offset on, followed live
// when follow is set.
func (c *Client) openLogs(ctx context.Context, id string, offset int64, follow bool) (*http.Response, error) {
	query := url.Values{}

	if offset > 0 {
		query.Set("offset", strconv.FormatInt(offset, 10))
	}

	if follow {
		query.Set("follow", "true")
	}

	return c.send(ctx, http.MethodGet, "/v1/execution/"+url.PathEscape(id)+"/logs", query, nil)
}

// Cancel stops an execution, and the whole run when the server is running it.
func (c *Client) Cancel(ctx context.Context, id string) (*Execution, error) {
	var out Execution

	return &out, c.do(ctx, http.MethodDelete, "/v1/execution/"+url.PathEscape(id), "", nil, &out)
}

// -------------------------------------------------------------------------
// NODES
// -------------------------------------------------------------------------

// Nodes lists the client nodes connected to the server.
func (c *Client) Nodes(ctx context.Context) ([]NodeListStub, error) {
	var out []NodeListStub

	if err := c.do(ctx, http.MethodGet, "/v1/nodes", "", nil, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// -------------------------------------------------------------------------
// PROVIDERS
// -------------------------------------------------------------------------

// Providers lists the configured providers and what each has used.
func (c *Client) Providers(ctx context.Context) (*ProviderList, error) {
	var out ProviderList

	if err := c.do(ctx, http.MethodGet, "/v1/providers", "", nil, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// -------------------------------------------------------------------------
// HEALTH
// -------------------------------------------------------------------------

// Health reads whether the server can take new work. A degraded server answers
// 503 with its health as the body, which comes back beside the error.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	target := c.base.JoinPath("/v1/health")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("GET /v1/health: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting the server at %s: %w", c.base, err)
	}

	defer func() { _ = resp.Body.Close() }()

	var out Health
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("GET /v1/health: decoding the answer: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return &out, &ResponseError{Status: resp.StatusCode, Body: Error{Error: out.StoreError}}
	}

	return &out, nil
}

// -------------------------------------------------------------------------
// TRANSPORT
// -------------------------------------------------------------------------

// do sends body as JSON, as ?namespace= when namespace is set, and decodes a
// 2xx answer into out.
func (c *Client) do(ctx context.Context, method, path, namespace string, body, out any) error {
	var query url.Values

	if namespace != "" {
		query = url.Values{"namespace": {namespace}}
	}

	resp, err := c.send(ctx, method, path, query, body)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decoding the answer: %w", method, path, err)
	}

	return nil
}

// send makes one request with query, and turns a non-2xx answer into a
// *ResponseError. The caller closes a 2xx body.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	target := c.base.JoinPath(path)
	target.RawQuery = query.Encode()

	var reader io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding the request: %w", err)
		}

		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting the server at %s: %w", c.base, err)
	}

	if resp.StatusCode/100 == 2 {
		return resp, nil
	}

	defer func() { _ = resp.Body.Close() }()

	failure := &ResponseError{Status: resp.StatusCode}
	if err := json.NewDecoder(resp.Body).Decode(&failure.Body); err != nil || failure.Body.Error == "" {
		failure.Body.Error = resp.Status
	}

	return nil, failure
}
