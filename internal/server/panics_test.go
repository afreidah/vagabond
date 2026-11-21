// -------------------------------------------------------------------------------
// Panic Tests
//
// Author: Alex Freidah
//
// A handler's panic is a 500 in the API's error body, or a cut connection once
// its response has begun; an upkeep task's panic leaves the next tick to run;
// and a panic while following output stops the follow.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/state/memory"
	"github.com/afreidah/vagabond/internal/testkit"
)

// quiet is a server with only a logger that discards.
func quiet() *Server {
	return &Server{logger: slog.New(slog.DiscardHandler)}
}

// get requests url with the test's context.
func get(t *testing.T, url string) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	return http.DefaultClient.Do(req)
}

// A handler that panics before writing answers 500 with the API's error body,
// not the panic.
func TestRecoverRequests_AnswersFiveHundred(t *testing.T) {
	srv := httptest.NewServer(quiet().recoverRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret detail")
	})))
	t.Cleanup(srv.Close)

	resp, err := get(t, srv.URL)
	if err != nil {
		t.Fatalf("GET = %v", err)
	}
	defer resp.Body.Close()

	var body api.Error
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding = %v", err)
	}

	if resp.StatusCode != http.StatusInternalServerError || body.Error != "internal error" {
		t.Errorf("answer = %d %q, want 500 internal error and nothing of the panic", resp.StatusCode, body.Error)
	}
}

// A handler that panics after its response began, flushed through the
// wrapper as a live stream is, cuts the connection, so the client sees the
// response end early rather than a wrong body.
func TestRecoverRequests_CutsAResponseUnderWay(t *testing.T) {
	srv := httptest.NewServer(quiet().recoverRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))

		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush() = %v, want it passed through", err)
		}

		panic("mid-stream")
	})))
	t.Cleanup(srv.Close)

	resp, err := get(t, srv.URL)
	if err != nil {
		t.Fatalf("GET = %v", err)
	}
	defer resp.Body.Close()

	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("the body read whole, want the connection cut")
	}
}

// A request that does not panic passes through untouched.
func TestRecoverRequests_PassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()

	quiet().recoverRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want the handler's", rec.Code)
	}
}

// An upkeep task that panics, or returns an error, is called again on the
// next tick.
func TestEvery_RunsOnAfterAPanicOrAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var calls atomic.Int32

	go quiet().every(ctx, "a flaky task", time.Millisecond, func(context.Context) error {
		switch calls.Add(1) {
		case 1:
			panic("first tick")
		case 2:
			return errors.New("second tick")
		default:
			return nil
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	if calls.Load() < 3 {
		t.Errorf("called %d times, want the loop to go on after the panic", calls.Load())
	}
}

// panickingGets is a memory store whose reads panic.
type panickingGets struct {
	*memory.Executions
}

// Get panics.
func (panickingGets) Get(context.Context, execution.ID) (*execution.Record, error) {
	panic("unreadable record")
}

// A panic while watching for an execution's end stops the follow.
func TestEndFollow_PanicStopsTheFollow(t *testing.T) {
	s := quiet()
	s.executions = panickingGets{memory.NewExecutions()}
	s.followPoll = time.Millisecond
	s.stopping = make(chan struct{})

	stopped := make(chan struct{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.endFollow(t.Context(), func() { close(stopped) }, testkit.NewID(t))
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the follow was not stopped after a panic")
	}

	<-done
}
