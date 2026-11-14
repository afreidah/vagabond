// -------------------------------------------------------------------------------
// API Client Tests
//
// Author: Alex Freidah
//
// How the client reads a server's answers and failures, and how a retrying
// copy rides out an outage: what it retries, what it leaves alone, and how it
// gives up. Each test runs a stub server that counts what it was sent.
// -------------------------------------------------------------------------------

package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// quick retries without waiting long, counting the retries it starts.
func quick(retried *atomic.Int32) Retries {
	return Retries{Wait: time.Millisecond, MaxWait: 2 * time.Millisecond, Attempts: 3, Retrying: func() { retried.Add(1) }}
}

// stub is a client for a server whose nth request, counting from one, gets
// answer(n, w, r), and the count of requests it served.
func stub(t *testing.T, answer func(n int32, w http.ResponseWriter, r *http.Request)) (*Client, *atomic.Int32) {
	t.Helper()

	var served atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer(served.Add(1), w, r)
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, nil)
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}

	return client, &served
}

// reply writes status and body as the server's JSON would.
func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// drop closes the connection without an answer, as a server going down does.
func drop(t *testing.T, w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)

		return
	}

	_ = conn.Close()
}

// -------------------------------------------------------------------------
// RETRYING
// -------------------------------------------------------------------------

// A read the server leaves unanswered, by dropping it or answering 502, 503
// or 504, is retried until it answers, with one notice per read.
func TestRetrying_RidesOutAnOutage(t *testing.T) {
	failures := map[string]func(t *testing.T, w http.ResponseWriter){
		"dropped":     drop,
		"bad gateway": func(_ *testing.T, w http.ResponseWriter) { reply(w, http.StatusBadGateway, "") },
		"unavailable": func(_ *testing.T, w http.ResponseWriter) { reply(w, http.StatusServiceUnavailable, "") },
		"timeout":     func(_ *testing.T, w http.ResponseWriter) { reply(w, http.StatusGatewayTimeout, "") },
	}

	for name, fail := range failures {
		t.Run(name, func(t *testing.T) {
			client, served := stub(t, func(n int32, w http.ResponseWriter, _ *http.Request) {
				if n <= 2 {
					fail(t, w)

					return
				}

				reply(w, http.StatusOK, `{"DispatchID":"d-1","State":"running"}`)
			})

			var retried atomic.Int32

			d, err := client.Retrying(quick(&retried)).DispatchStatus(t.Context(), "d-1")
			if err != nil || d.DispatchID != "d-1" {
				t.Fatalf("DispatchStatus() = %+v, %v; want the answer once the server is back", d, err)
			}

			if served.Load() != 3 || retried.Load() != 1 {
				t.Errorf("served %d requests with %d notices, want 3 and 1", served.Load(), retried.Load())
			}
		})
	}
}

// A server down past the retries ends in its last answer, which says the
// server is not answering.
func TestRetrying_GivesUp(t *testing.T) {
	client, served := stub(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusServiceUnavailable, `{"Error":"store unreachable"}`)
	})

	var retried atomic.Int32

	_, err := client.Retrying(quick(&retried)).DispatchStatus(t.Context(), "d-1")

	failure, ok := errors.AsType[*ResponseError](err)
	if !ok || failure.Status != http.StatusServiceUnavailable || failure.Error() != "store unreachable" || !Unanswered(err) {
		t.Errorf("DispatchStatus() = %v, want the last 503 as an unanswered failure", err)
	}

	if served.Load() != 4 {
		t.Errorf("served %d requests, want the first and three retries", served.Load())
	}
}

// An answer that will not change is not retried, and a submission is never
// repeated, whatever came back.
func TestRetrying_LeavesAlone(t *testing.T) {
	tests := map[string]struct {
		status int
		call   func(c *Client) error
	}{
		"not found": {http.StatusNotFound, func(c *Client) error {
			_, err := c.DispatchStatus(t.Context(), "d-1")

			return err
		}},
		"bad request": {http.StatusBadRequest, func(c *Client) error {
			_, err := c.Execution(t.Context(), "e-1")

			return err
		}},
		"server fault": {http.StatusInternalServerError, func(c *Client) error {
			_, err := c.Execution(t.Context(), "e-1")

			return err
		}},
		"a submission": {http.StatusServiceUnavailable, func(c *Client) error {
			_, err := c.Run(t.Context(), "", "job {}", nil)

			return err
		}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			client, served := stub(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
				reply(w, tt.status, `{"Error":"no"}`)
			})

			var retried atomic.Int32

			if err := tt.call(client.Retrying(quick(&retried))); err == nil {
				t.Fatal("the call succeeded")
			}

			if served.Load() != 1 || retried.Load() != 0 {
				t.Errorf("served %d requests with %d notices, want one and none", served.Load(), retried.Load())
			}
		})
	}
}

// A retrying client stops once its caller gives up, whether it was waiting
// to retry or waiting on an answer.
func TestRetrying_StopsWithTheCaller(t *testing.T) {
	answers := map[string]func(w http.ResponseWriter, r *http.Request){
		"waiting to retry": func(w http.ResponseWriter, _ *http.Request) { reply(w, http.StatusServiceUnavailable, "") },
		"waiting on an answer": func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		},
	}

	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			client, served := stub(t, func(_ int32, w http.ResponseWriter, r *http.Request) { answer(w, r) })

			slow := Retries{Wait: time.Hour, MaxWait: time.Hour, Attempts: 3}

			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()

			if _, err := client.Retrying(slow).DispatchStatus(ctx, "d-1"); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("DispatchStatus() = %v, want the caller's deadline", err)
			}

			if served.Load() != 1 {
				t.Errorf("served %d requests, want the first only", served.Load())
			}
		})
	}
}

// -------------------------------------------------------------------------
// FAILURES
// -------------------------------------------------------------------------

// A server nothing answers for is named in the error, and is unanswered.
func TestClient_NoAnswer(t *testing.T) {
	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	addr := l.Addr().String()
	_ = l.Close()

	client, err := NewClient(addr, nil)
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}

	got, err := client.Execution(t.Context(), "e-1")
	if got != nil || !Unanswered(err) || !strings.Contains(err.Error(), "contacting the server at http://"+addr) {
		t.Errorf("Execution() = %v, %v; want nothing and an unanswered failure naming the server", got, err)
	}

	if client.Address() != "http://"+addr {
		t.Errorf("Address() = %q", client.Address())
	}
}

// A refusal without the server's JSON body still says what came back.
func TestClient_RefusalWithoutABody(t *testing.T) {
	client, _ := stub(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := client.Jobs(t.Context(), "")

	if failure, ok := errors.AsType[*ResponseError](err); !ok || failure.Error() != "502 Bad Gateway" {
		t.Errorf("Jobs() = %v, want the status as the message", err)
	}
}

// A degraded server's 503 carries its health beside the error; any other
// refusal is a plain failure.
func TestClient_Health(t *testing.T) {
	degraded, _ := stub(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusServiceUnavailable, `{"Store":"unreachable","StoreError":"dial tcp: refused"}`)
	})

	health, err := degraded.Health(t.Context())
	if health == nil || health.Store != "unreachable" || err == nil || err.Error() != "dial tcp: refused" {
		t.Errorf("Health() = %+v, %v; want the degraded health and its store error", health, err)
	}

	proxy, _ := stub(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusServiceUnavailable, "upstream gone")
	})

	health, err = proxy.Health(t.Context())

	if failure, ok := errors.AsType[*ResponseError](err); health != nil || !ok || failure.Status != http.StatusServiceUnavailable {
		t.Errorf("Health() = %+v, %v; want only the proxy's 503", health, err)
	}
}

// An answer that does not decode names the request.
func TestClient_UndecodableAnswer(t *testing.T) {
	client, _ := stub(t, func(_ int32, w http.ResponseWriter, _ *http.Request) {
		reply(w, http.StatusOK, "not json")
	})

	if _, err := client.Providers(t.Context()); err == nil || !strings.HasPrefix(err.Error(), "GET /v1/providers: decoding the answer") {
		t.Errorf("Providers() = %v, want a decoding failure naming the request", err)
	}
}
