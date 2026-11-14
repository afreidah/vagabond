// -------------------------------------------------------------------------------
// REST Request Tests
//
// Author: Alex Freidah
//
// What goes out: the payload or body and the headers. What comes back: a 2xx
// streamed, read or decoded, and each kind of failure a caller tells apart, a
// refusal with its status and the start of its body, no answer, or a request
// or answer the caller got wrong.
// -------------------------------------------------------------------------------

package rest

import (
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// seen is what a test server received.
type seen struct {
	method, accept, contentType, body string
}

// serve answers every request with status and body after recording it, until
// the test ends.
func serve(t *testing.T, status int, body string, header http.Header) (string, *seen) {
	t.Helper()

	got := &seen{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ := io.ReadAll(r.Body)
		*got = seen{r.Method, r.Header.Get("Accept"), r.Header.Get("Content-Type"), string(received)}

		maps.Copy(w.Header(), header)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	return server.URL, got
}

// opened makes req with Open, closing any answer, and returns the failure.
func opened(t *testing.T, req *Request) error {
	t.Helper()

	resp, err := Open(t.Context(), http.DefaultClient, req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	return err
}

// -------------------------------------------------------------------------
// REQUESTS
// -------------------------------------------------------------------------

// A payload goes out as JSON, accepting JSON, and the answer is decoded.
func TestCall_SendsAndDecodesJSON(t *testing.T) {
	url, got := serve(t, http.StatusOK, `{"name":"build"}`, nil)

	var out struct{ Name string }

	err := Call(t.Context(), http.DefaultClient, &Request{Method: http.MethodPost, URL: url, Payload: map[string]int{"n": 1}}, &out)
	if err != nil || out.Name != "build" {
		t.Fatalf("Call() = %v, %+v; want the decoded answer", err, out)
	}

	if got.method != http.MethodPost || got.body != `{"n":1}` || got.contentType != mediaJSON || got.accept != mediaJSON {
		t.Errorf("server saw %+v, want a JSON POST accepting JSON", got)
	}
}

// A body goes out as its own content type, and a header's Accept stands.
func TestOpen_SendsABodyAsGiven(t *testing.T) {
	url, got := serve(t, http.StatusOK, "", nil)

	resp, err := Open(t.Context(), http.DefaultClient, &Request{
		Method: http.MethodPost, URL: url, Header: http.Header{"Accept": {"text/yaml"}},
		Body: strings.NewReader("a=1"), ContentType: "application/x-www-form-urlencoded",
	})
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	_ = resp.Body.Close()

	if got.body != "a=1" || got.contentType != "application/x-www-form-urlencoded" || got.accept != "text/yaml" {
		t.Errorf("server saw %+v, want the form with the caller's Accept", got)
	}
}

// Read stops at the limit, and a nil out discards the answer.
func TestRead_HonoursTheLimit(t *testing.T) {
	url, _ := serve(t, http.StatusOK, "0123456789", nil)

	body, err := Read(t.Context(), http.DefaultClient, &Request{Method: http.MethodGet, URL: url, Limit: 4})
	if err != nil || string(body) != "0123" {
		t.Errorf("Read() = %q, %v; want the first four bytes", body, err)
	}

	if err := Call(t.Context(), http.DefaultClient, &Request{Method: http.MethodGet, URL: url}, nil); err != nil {
		t.Errorf("Call() with no out = %v", err)
	}
}

// -------------------------------------------------------------------------
// FAILURES
// -------------------------------------------------------------------------

// A non-2xx is a Refusal with the request, the status, the headers and the
// start of the body, which is all its caller needs to classify it.
func TestOpen_RefusalKeepsWhatCameBack(t *testing.T) {
	long := strings.Repeat("x", maxRefusal+100)
	url, _ := serve(t, http.StatusTooManyRequests, long, http.Header{"Retry-After": {"30"}})

	err := opened(t, &Request{Method: http.MethodGet, URL: url + "/runs"})

	r, ok := errors.AsType[*Refusal](err)
	if !ok {
		t.Fatalf("Open() = %v, want a Refusal", err)
	}

	if r.Status != http.StatusTooManyRequests || r.URL.Path != "/runs" || len(r.Body) != maxRefusal || r.RetryAfter() != 30*time.Second {
		t.Errorf("refusal = %d %s, %d bytes, retry after %s", r.Status, r.URL.Path, len(r.Body), r.RetryAfter())
	}

	if want := "GET /runs: 429 Too Many Requests"; r.Error() != want {
		t.Errorf("Error() = %q, want %q", r.Error(), want)
	}
}

// Retry-After is read as whole seconds; an absent, malformed or negative value
// is zero, leaving the wait to the caller.
func TestRefusal_RetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"30":   30 * time.Second,
		"0":    0,
		"":     0,
		"soon": 0,
		"-5":   0,
	}

	for header, want := range cases {
		r := &Refusal{Header: http.Header{}}
		if header != "" {
			r.Header.Set("Retry-After", header)
		}

		if got := r.RetryAfter(); got != want {
			t.Errorf("RetryAfter(%q) = %v, want %v", header, got, want)
		}
	}
}

// A call nothing answers is ErrUnreachable, with the transport's own words.
func TestOpen_NoAnswer(t *testing.T) {
	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	addr := l.Addr().String()
	_ = l.Close()

	err = opened(t, &Request{Method: http.MethodGet, URL: "http://" + addr})
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), addr) {
		t.Errorf("Open() on a closed port = %v, want ErrUnreachable naming the address", err)
	}
}

// A body cut off partway got no whole answer either.
func TestRead_CutOffIsNoAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "short")
	}))
	t.Cleanup(server.Close)

	if _, err := Read(t.Context(), http.DefaultClient, &Request{Method: http.MethodGet, URL: server.URL}); !errors.Is(err, ErrUnreachable) {
		t.Errorf("Read() of a cut-off body = %v, want ErrUnreachable", err)
	}
}

// A request that cannot be built, or an answer that cannot be decoded, is the
// caller's own fault: neither a refusal nor no answer.
func TestCall_CallersOwnFaults(t *testing.T) {
	url, _ := serve(t, http.StatusOK, "not json", nil)

	tests := map[string]*Request{
		"unencodable payload": {Method: http.MethodPost, URL: url, Payload: func() {}},
		"unparsable URL":      {Method: http.MethodGet, URL: "http://bad host"},
		"undecodable answer":  {Method: http.MethodGet, URL: url},
	}

	for name, req := range tests {
		var out struct{}

		err := Call(t.Context(), http.DefaultClient, req, &out)

		_, refused := errors.AsType[*Refusal](err)
		if err == nil || refused || errors.Is(err, ErrUnreachable) {
			t.Errorf("%s: Call() = %v, want a fault of the caller's", name, err)
		}
	}
}
