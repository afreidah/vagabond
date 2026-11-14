// -------------------------------------------------------------------------------
// REST Requests
//
// Author: Alex Freidah
//
// One request and its answer: a JSON payload or a body of another type out, a
// 2xx answer back, streamed, read or decoded, and anything else kept as a
// Refusal for the caller to classify.
// -------------------------------------------------------------------------------

package rest

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
	"strconv"
	"time"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// mediaJSON is what a payload is sent as, and what every call accepts unless
// its header says otherwise.
const mediaJSON = "application/json"

// maxRefusal is how much of a refusal's body is kept: enough for any
// platform's message, never a whole page an error proxy sent.
const maxRefusal = 64 << 10

// ErrUnreachable marks a call that got no whole answer: a connection refused
// or broken, a name that did not resolve, a timeout, or a body cut off.
var ErrUnreachable = errors.New("no answer")

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Request is one call. Payload, when set, is sent as JSON; otherwise Body, if
// any, is sent as ContentType. Header adds to the request, Accept defaulting
// to JSON. Limit bounds how much of a 2xx body Read and Call take; zero takes
// it all.
type Request struct {
	Method      string
	URL         string
	Header      http.Header
	Payload     any
	Body        io.Reader
	ContentType string
	Limit       int64
}

// Refusal is a non-2xx answer: the request's method and URL, the status, the
// answer's headers, and the start of its body.
type Refusal struct {
	Method string
	URL    *url.URL
	Status int
	Header http.Header
	Body   []byte
}

// Error names the request and the status, which is all a refusal says before
// its caller reads the body.
func (r *Refusal) Error() string {
	return fmt.Sprintf("%s %s: %d %s", r.Method, r.URL.Path, r.Status, http.StatusText(r.Status))
}

// RetryAfter is how long the answer asked the caller to wait, in whole
// seconds; zero when it did not say.
func (r *Refusal) RetryAfter() time.Duration {
	seconds, err := strconv.Atoi(r.Header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0
	}

	return time.Duration(seconds) * time.Second
}

// unreachable is a call that got no whole answer. It adds no text of its own,
// so the cause reads as the transport put it.
type unreachable struct {
	err error
}

// Error is the cause's own message.
func (u *unreachable) Error() string {
	return u.err.Error()
}

// Unwrap reaches the cause, so a context's error is still found under it.
func (u *unreachable) Unwrap() error {
	return u.err
}

// Is matches ErrUnreachable, which is how callers ask.
func (u *unreachable) Is(target error) bool {
	return target == ErrUnreachable
}

// -------------------------------------------------------------------------
// REQUESTS
// -------------------------------------------------------------------------

// Open sends req on client and returns a 2xx answer with its body unread, for
// a caller that streams it. The caller closes the body.
func Open(ctx context.Context, client *http.Client, req *Request) (*http.Response, error) {
	httpReq, err := build(ctx, req)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, &unreachable{err: err}
	}

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return resp, nil
	}

	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxRefusal))

	return nil, &Refusal{Method: req.Method, URL: httpReq.URL, Status: resp.StatusCode, Header: resp.Header, Body: body}
}

// Read sends req on client and returns a 2xx answer's body, up to req.Limit.
// A body cut off partway is a call that got no whole answer.
func Read(ctx context.Context, client *http.Client, req *Request) ([]byte, error) {
	resp, err := Open(ctx, client, req)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	var body io.Reader = resp.Body
	if req.Limit > 0 {
		body = io.LimitReader(resp.Body, req.Limit)
	}

	received, err := io.ReadAll(body)
	if err != nil {
		return nil, &unreachable{err: fmt.Errorf("reading the answer: %w", err)}
	}

	return received, nil
}

// Call sends req on client and decodes a 2xx answer's JSON into out. A nil out
// discards the answer.
func Call(ctx context.Context, client *http.Client, req *Request, out any) error {
	received, err := Read(ctx, client, req)
	if err != nil || out == nil {
		return err
	}

	if err := json.Unmarshal(received, out); err != nil {
		return fmt.Errorf("decoding the answer: %w", err)
	}

	return nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// build turns req into an HTTP request on ctx, encoding its payload. A failure
// is the caller's: a payload that does not encode or a URL that does not
// parse.
func build(ctx context.Context, req *Request) (*http.Request, error) {
	body, contentType := req.Body, req.ContentType

	if req.Payload != nil {
		encoded, err := json.Marshal(req.Payload)
		if err != nil {
			return nil, fmt.Errorf("encoding the request: %w", err)
		}

		body, contentType = bytes.NewReader(encoded), mediaJSON
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}

	maps.Copy(httpReq.Header, req.Header)

	if httpReq.Header.Get("Accept") == "" {
		httpReq.Header.Set("Accept", mediaJSON)
	}

	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}

	return httpReq, nil
}
