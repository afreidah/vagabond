// -------------------------------------------------------------------------------
// API Client
//
// Author: Alex Freidah
//
// One method per /v1 route, taking and returning the types in this package,
// over the shared rest path. A non-2xx answer comes back as a *ResponseError
// carrying the status and the server's api.Error body, diagnostics included.
// A client following a run can retry the reads a server outage interrupts.
// -------------------------------------------------------------------------------

package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-cleanhttp"
	"github.com/hashicorp/go-retryablehttp"

	"github.com/afreidah/vagabond/internal/backoff"
	"github.com/afreidah/vagabond/internal/rest"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// DefaultAddress is where a client looks for a server when told nothing else,
// matching the server's default bind.
const DefaultAddress = "http://127.0.0.1:4747"

// DefaultRetries rides out about two minutes without an answer, long enough
// for a server to be redeployed under a run that keeps going.
var DefaultRetries = Retries{Wait: time.Second, MaxWait: 15 * time.Second, Attempts: 10}

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

// Retries is how a client retries a GET the server did not answer, or
// answered 429, 502, 503 or 504: up to Attempts more tries, waiting Wait
// doubling to MaxWait. Retrying, when set, runs before a request's first
// retry.
type Retries struct {
	Wait     time.Duration
	MaxWait  time.Duration
	Attempts int
	Retrying func()
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

// Address is the server this client talks to.
func (c *Client) Address() string {
	return c.base.String()
}

// Retrying returns a copy of the client that retries as r says. Only a GET is
// retried, so nothing is ever submitted twice, and the last answer comes back
// as any answer would.
func (c *Client) Retrying(r Retries) *Client {
	retrying := retryablehttp.NewClient()
	retrying.HTTPClient = c.http
	retrying.Logger = nil
	retrying.RetryWaitMin, retrying.RetryWaitMax, retrying.RetryMax = r.Wait, r.MaxWait, r.Attempts
	retrying.CheckRetry = retryPolicy
	retrying.ErrorHandler = retryablehttp.PassthroughErrorHandler

	retrying.Backoff = func(wait, ceiling time.Duration, n int, _ *http.Response) time.Duration {
		return backoff.Exponential(wait, ceiling, n)
	}

	retrying.RequestLogHook = func(_ retryablehttp.Logger, _ *http.Request, attempt int) {
		if attempt == 1 && r.Retrying != nil {
			r.Retrying()
		}
	}

	gets := &getsRetried{retrying: &retryablehttp.RoundTripper{Client: retrying}, plain: c.http.Transport}

	return &Client{base: c.base, http: &http.Client{Transport: gets}}
}

// -------------------------------------------------------------------------
// JOBS
// -------------------------------------------------------------------------

// Register stores a job file as a new version when it changed.
func (c *Client) Register(ctx context.Context, namespace, source string) (*RegisterResponse, error) {
	return call[RegisterResponse](ctx, c, http.MethodPost, "/v1/jobs", namespace, RegisterRequest{Source: source})
}

// Jobs lists every job registered in namespace, stopped ones included.
func (c *Client) Jobs(ctx context.Context, namespace string) ([]Job, error) {
	jobs, err := call[[]Job](ctx, c, http.MethodGet, "/v1/jobs", namespace, nil)
	if err != nil {
		return nil, err
	}

	return *jobs, nil
}

// JobStatus reads a registered job with its versions and recent executions.
func (c *Client) JobStatus(ctx context.Context, namespace, name string) (*JobStatus, error) {
	return call[JobStatus](ctx, c, http.MethodGet, jobPath(name), namespace, nil)
}

// StopJob deregisters a job so it can no longer be dispatched.
func (c *Client) StopJob(ctx context.Context, namespace, name string) (*Job, error) {
	return call[Job](ctx, c, http.MethodDelete, jobPath(name), namespace, nil)
}

// -------------------------------------------------------------------------
// RUNS
// -------------------------------------------------------------------------

// Dispatch starts a run of a registered job's current version.
func (c *Client) Dispatch(
	ctx context.Context, namespace, name string, meta map[string]string,
) (*DispatchResponse, error) {
	return call[DispatchResponse](ctx, c, http.MethodPost, jobPath(name)+"/dispatch", namespace, DispatchRequest{Meta: meta})
}

// Run starts a run of a job file without registering it.
func (c *Client) Run(
	ctx context.Context, namespace, source string, meta map[string]string,
) (*DispatchResponse, error) {
	return call[DispatchResponse](ctx, c, http.MethodPost, "/v1/jobs/run", namespace, RunRequest{Source: source, Meta: meta})
}

// Plan shows where each task of a job file or registered job would run.
func (c *Client) Plan(ctx context.Context, namespace string, req PlanRequest) (*Plan, error) {
	return call[Plan](ctx, c, http.MethodPost, "/v1/jobs/plan", namespace, req)
}

// DispatchStatus reads a run and the executions it has created so far.
func (c *Client) DispatchStatus(ctx context.Context, id string) (*Dispatch, error) {
	return call[Dispatch](ctx, c, http.MethodGet, "/v1/dispatch/"+url.PathEscape(id), "", nil)
}

// -------------------------------------------------------------------------
// EXECUTIONS
// -------------------------------------------------------------------------

// Execution reads one attempt.
func (c *Client) Execution(ctx context.Context, id string) (*Execution, error) {
	return call[Execution](ctx, c, http.MethodGet, executionPath(id), "", nil)
}

// Logs copies an execution's stored output from byte offset on to w.
func (c *Client) Logs(ctx context.Context, id string, offset int64, w io.Writer) error {
	return c.copyLogs(ctx, id, offset, false, w)
}

// FollowLogs copies a running execution's output from byte offset on to w as
// it is written, until the server ends the stream. An error means the stream
// broke rather than ended.
func (c *Client) FollowLogs(ctx context.Context, id string, offset int64, w io.Writer) error {
	return c.copyLogs(ctx, id, offset, true, w)
}

// copyLogs copies an execution's output from byte offset on to w, followed
// live when follow is set.
func (c *Client) copyLogs(ctx context.Context, id string, offset int64, follow bool, w io.Writer) error {
	query := url.Values{}

	if offset > 0 {
		query.Set("offset", strconv.FormatInt(offset, 10))
	}

	if follow {
		query.Set("follow", "true")
	}

	path := executionPath(id) + "/logs"

	resp, err := rest.Open(ctx, c.http, c.request(http.MethodGet, path, query, nil))
	if err != nil {
		return c.failure(http.MethodGet, path, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("reading the output of %s: %w", id, err)
	}

	return nil
}

// Cancel stops an execution, and the whole run when the server is running it.
func (c *Client) Cancel(ctx context.Context, id string) (*Execution, error) {
	return call[Execution](ctx, c, http.MethodDelete, executionPath(id), "", nil)
}

// -------------------------------------------------------------------------
// NODES
// -------------------------------------------------------------------------

// Nodes lists the client nodes connected to the server.
func (c *Client) Nodes(ctx context.Context) ([]NodeListStub, error) {
	nodes, err := call[[]NodeListStub](ctx, c, http.MethodGet, "/v1/nodes", "", nil)
	if err != nil {
		return nil, err
	}

	return *nodes, nil
}

// -------------------------------------------------------------------------
// PROVIDERS
// -------------------------------------------------------------------------

// Providers lists the configured providers and what each has used.
func (c *Client) Providers(ctx context.Context) (*ProviderList, error) {
	return call[ProviderList](ctx, c, http.MethodGet, "/v1/providers", "", nil)
}

// -------------------------------------------------------------------------
// HEALTH
// -------------------------------------------------------------------------

// Health reads whether the server can take new work. A degraded server answers
// 503 with its health as the body, which comes back beside the error; any
// other refusal is an error like any other.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	const path = "/v1/health"

	var out Health

	err := rest.Call(ctx, c.http, c.request(http.MethodGet, path, nil, nil), &out)

	if r, ok := errors.AsType[*rest.Refusal](err); ok && json.Unmarshal(r.Body, &out) == nil && out.Store != "" {
		return &out, &ResponseError{Status: r.Status, Body: Error{Error: out.StoreError}}
	}

	if err != nil {
		return nil, c.failure(http.MethodGet, path, err)
	}

	return &out, nil
}

// -------------------------------------------------------------------------
// RETRYING
// -------------------------------------------------------------------------

// Unanswered reports whether err is the server not answering, or answering
// 429, 502, 503 or 504: what a retrying client retries, and what may pass.
func Unanswered(err error) bool {
	if failure, ok := errors.AsType[*ResponseError](err); ok {
		return passing(failure.Status)
	}

	return errors.Is(err, rest.ErrUnreachable)
}

// retryPolicy retries what Unanswered names, never once the caller gave up. A
// transport error goes to the library's own policy, which declines what no
// retry changes: a certificate the client does not trust, a bad scheme or
// header, too many redirects.
func retryPolicy(ctx context.Context, resp *http.Response, err error) (bool, error) {
	switch {
	case ctx.Err() != nil:
		return false, ctx.Err()
	case err != nil:
		return retryablehttp.DefaultRetryPolicy(ctx, resp, err)
	default:
		return passing(resp.StatusCode), nil
	}
}

// passing reports whether status is a server too busy, or a proxy with no
// server behind it: answers that change once the server is back.
func passing(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// getsRetried sends a GET through the retrying transport and anything else
// through the plain one, so no submission is repeated.
type getsRetried struct {
	retrying http.RoundTripper
	plain    http.RoundTripper
}

// RoundTrip picks the transport by method.
func (t *getsRetried) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet {
		return t.retrying.RoundTrip(req)
	}

	return t.plain.RoundTrip(req)
}

// -------------------------------------------------------------------------
// TRANSPORT
// -------------------------------------------------------------------------

// call sends body as JSON, as ?namespace= when namespace is set, and decodes a
// 2xx answer into a new T, nil on any failure.
func call[T any](ctx context.Context, c *Client, method, path, namespace string, body any) (*T, error) {
	var query url.Values

	if namespace != "" {
		query = url.Values{"namespace": {namespace}}
	}

	out := new(T)

	if err := rest.Call(ctx, c.http, c.request(method, path, query, body), out); err != nil {
		return nil, c.failure(method, path, err)
	}

	return out, nil
}

// request is a rest request for path on this client's server, with query.
func (c *Client) request(method, path string, query url.Values, body any) *rest.Request {
	target := c.base.JoinPath(path)
	target.RawQuery = query.Encode()

	return &rest.Request{Method: method, URL: target.String(), Payload: body}
}

// failure turns a refusal into a *ResponseError carrying the server's body, a
// call that got no answer into one naming the server, and anything else into
// one naming the request.
func (c *Client) failure(method, path string, err error) error {
	if r, ok := errors.AsType[*rest.Refusal](err); ok {
		failure := &ResponseError{Status: r.Status}
		if json.Unmarshal(r.Body, &failure.Body) != nil || failure.Body.Error == "" {
			failure.Body.Error = fmt.Sprintf("%d %s", r.Status, http.StatusText(r.Status))
		}

		return failure
	}

	if errors.Is(err, rest.ErrUnreachable) {
		return fmt.Errorf("contacting the server at %s: %w", c.base, err)
	}

	return fmt.Errorf("%s %s: %w", method, path, err)
}

// jobPath is the route of one registered job.
func jobPath(name string) string {
	return "/v1/job/" + url.PathEscape(name)
}

// executionPath is the route of one execution.
func executionPath(id string) string {
	return "/v1/execution/" + url.PathEscape(id)
}
