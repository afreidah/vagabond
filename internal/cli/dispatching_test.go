// -------------------------------------------------------------------------------
// Follower Tests
//
// Author: Alex Freidah
//
// The parts of following a run that a real server cannot be made to produce on
// demand: a stream that cannot resume, a stored copy missing its front, cut by
// the provider or unreadable,
// an execution that ends with no result, a platform's reason for ending one,
// and a server that stops answering mid-run. A stub answers the routes.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/cli"

	"github.com/afreidah/vagabond/internal/api"
)

// -------------------------------------------------------------------------
// HARNESS
// -------------------------------------------------------------------------

// stubLogs is a client for a server whose logs route answers with answer,
// given the offset asked for.
func stubLogs(t *testing.T, answer func(offset string) (int, string)) *api.Client {
	t.Helper()

	return stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		status, body := answer(r.URL.Query().Get("offset"))

		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
		}

		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// stubServer is a client for a server answering every request with handler,
// until the test ends.
func stubServer(t *testing.T, handler http.HandlerFunc) *api.Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := api.NewClient(server.URL, nil)
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}

	return client
}

// quickRetries shortens a follow's retries until the test ends.
func quickRetries(t *testing.T) {
	t.Helper()

	saved := followRetries
	followRetries = api.Retries{Wait: time.Millisecond, MaxWait: 2 * time.Millisecond, Attempts: 3}

	t.Cleanup(func() { followRetries = saved })
}

// failFirst answers the first n requests with status and the rest with body,
// as JSON.
func failFirst(n int32, status int, body string) http.HandlerFunc {
	var served atomic.Int32

	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if served.Add(1) <= n {
			w.WriteHeader(status)

			return
		}

		_, _ = io.WriteString(w, body)
	}
}

// -------------------------------------------------------------------------
// LIVE OUTPUT
// -------------------------------------------------------------------------

// A follow stops reopening the stream at the server's 416, keeping what it
// printed and leaving the rest to the stored copy.
func TestFollower_StopsWhenTheStreamCannotResume(t *testing.T) {
	client := stubLogs(t, func(offset string) (int, string) {
		if offset == "" {
			return http.StatusOK, "first\n"
		}

		return http.StatusRequestedRangeNotSatisfiable, `{"Error":"this provider's log stream cannot resume from an offset"}`
	})

	var stdout bytes.Buffer

	l := (&Meta{Stdout: &stdout}).newFollower(client, false).start(t.Context(), "exec-1")

	select {
	case <-l.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the follow kept reopening a stream that cannot resume")
	}

	if stdout.String() != "first\n" {
		t.Errorf("stdout %q, want the first stream's output", stdout.String())
	}
}

// The stored copy finishes what did not stream, from the bytes printed, and
// says when output is not there to print.
func TestFollower_Rest(t *testing.T) {
	tests := []struct {
		name      string
		streamed  string // what a follow already printed, empty for none
		start     int64  // the first byte the server kept
		truncated bool
		status    int
		stored    string
		wantOut   string
		wantWarn  string
	}{
		{"nothing streamed", "", 0, false, http.StatusOK, "all\n", "all\n", ""},
		{"output ends mid-line", "", 0, false, http.StatusOK, "partial", "partial\n", ""},
		{"the rest after a stream", "first\n", 0, false, http.StatusOK, "rest\n", "first\nrest\n", ""},
		{"everything streamed", "all\n", 0, false, http.StatusOK, "", "all\n", ""},
		{"front dropped, nothing streamed", "", 100, false, http.StatusOK, "tail\n", "tail\n", "100 bytes of output were not kept"},
		{"front dropped past the stream", "first\n", 100, false, http.StatusOK, "tail\n", "first\ntail\n", "94 bytes of output were not kept"},
		{"front dropped, all streamed", "first\n", 4, false, http.StatusOK, "", "first\n", ""},
		{"provider gave part", "", 0, true, http.StatusOK, "part\n", "part\n", "The provider gave only part of the output."},
		{"stored copy unreadable", "", 0, false, http.StatusInternalServerError, `{"Error":"boom"}`, "", "Could not read the output"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// An offset of zero is not sent at all.
			wantOffset := ""
			if n := len(tc.streamed); n > 0 {
				wantOffset = strconv.Itoa(n)
			}

			client := stubLogs(t, func(offset string) (int, string) {
				if offset != wantOffset {
					t.Errorf("stored copy read from offset %q, want %q", offset, wantOffset)
				}

				return tc.status, tc.stored
			})

			var stdout bytes.Buffer

			ui := cli.NewMockUi()
			f := (&Meta{Ui: ui, Stdout: &stdout}).newFollower(client, false)

			if tc.streamed != "" {
				done := make(chan struct{})
				close(done)

				l := &live{out: &output{mu: &f.stdout, w: &stdout}, end: func() {}, done: done}
				_, _ = l.out.Write([]byte(tc.streamed))
				f.live["exec-1"] = l
			}

			f.rest(t.Context(), &api.Execution{ID: "exec-1", LogsStart: tc.start, LogsTruncated: tc.truncated})

			warned := ui.ErrorWriter.String()

			if stdout.String() != tc.wantOut || (tc.wantWarn == "") != (warned == "") || !strings.Contains(warned, tc.wantWarn) {
				t.Errorf("stdout %q, warned %q; want %q and %q", stdout.String(), warned, tc.wantOut, tc.wantWarn)
			}
		})
	}
}

// watch follows an execution only once it leaves pending and never with
// -no-logs, and ends a follow whose execution ended with no result.
func TestFollower_Watch(t *testing.T) {
	f := (&Meta{}).newFollower(nil, false)

	f.watch(t.Context(), &api.Execution{ID: "pending", State: executionPending})

	if _, ok := f.live["pending"]; ok {
		t.Error("followed an execution that has not been submitted")
	}

	quiet := (&Meta{}).newFollower(nil, true)
	quiet.watch(t.Context(), &api.Execution{ID: "running", State: "running"})

	if len(quiet.live) != 0 {
		t.Error("followed output with -no-logs")
	}

	ended := false
	f.live["lost"] = &live{end: func() { ended = true }}

	f.watch(t.Context(), &api.Execution{ID: "lost", State: "lost", Ended: new(time.Now())})

	if !ended {
		t.Error("a follow was not told its execution ended")
	}
}

// -------------------------------------------------------------------------
// OUTAGES
// -------------------------------------------------------------------------

// A server that stops answering for a few polls does not end the follow: the
// run's own end is reported, after a notice that the server went quiet.
func TestFollow_RidesOutAnOutage(t *testing.T) {
	quickRetries(t)

	client := stubServer(t, failFirst(2, http.StatusServiceUnavailable, `{"DispatchID":"d-1","Job":"ci","State":"succeeded"}`))
	ui := cli.NewMockUi()

	code := (&Meta{Ui: ui}).follow(t.Context(), client, &api.DispatchResponse{DispatchID: "d-1", Job: "ci"}, true)

	if code != ExitSuccess || !strings.Contains(ui.ErrorWriter.String(), "Lost contact with the server at http://") {
		t.Errorf("exit %d, stderr:\n%s\nwant success after the notice", code, ui.ErrorWriter.String())
	}
}

// A server gone past the retries ends the follow, not the run: the error,
// then that the run may still be going and where to look.
func TestFollow_ServerGoneTooLong(t *testing.T) {
	quickRetries(t)

	running := `{"DispatchID":"d-1","Job":"ci","State":"running","Executions":[{"ID":"e-1","Task":"build","State":"running"}]}`

	var served atomic.Int32

	client := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if served.Add(1) == 1 {
			_, _ = io.WriteString(w, running)

			return
		}

		w.WriteHeader(http.StatusBadGateway)
	})

	ui := cli.NewMockUi()

	code := (&Meta{Ui: ui}).follow(t.Context(), client, &api.DispatchResponse{DispatchID: "d-1", Job: "ci"}, true)

	want := "Dispatch d-1 may still be running. Check it with vagabond execution status e-1."
	if code != ExitFailure || !strings.Contains(ui.ErrorWriter.String(), want) {
		t.Errorf("exit %d, stderr:\n%s\nwant failure saying %q", code, ui.ErrorWriter.String(), want)
	}
}

// A dispatch the server does not know is an answer that will not change: one
// read, and no suggestion that the run goes on.
func TestFollow_UnknownDispatchFailsAtOnce(t *testing.T) {
	quickRetries(t)

	var served atomic.Int32

	client := stubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"Error":"dispatch d-1 not found"}`)
	})

	ui := cli.NewMockUi()

	code := (&Meta{Ui: ui}).follow(t.Context(), client, &api.DispatchResponse{DispatchID: "d-1", Job: "ci"}, true)

	if code != ExitFailure || served.Load() != 1 || strings.Contains(ui.ErrorWriter.String(), "may still be running") {
		t.Errorf("exit %d after %d reads, stderr:\n%s\nwant one read and a plain failure", code, served.Load(), ui.ErrorWriter.String())
	}
}

// Following an execution's output gives up the same way, naming it.
func TestExecutionLogsFollow_ServerGoneTooLong(t *testing.T) {
	quickRetries(t)

	client := stubServer(t, failFirst(100, http.StatusServiceUnavailable, ""))
	ui := cli.NewMockUi()

	code := (&ExecutionLogsCommand{Meta: &Meta{Ui: ui}}).follow(t.Context(), client, "e-1")

	want := "Execution e-1 may still be running. Check it with vagabond execution status e-1."
	if code != ExitFailure || !strings.Contains(ui.ErrorWriter.String(), want) {
		t.Errorf("exit %d, stderr:\n%s\nwant failure saying %q", code, ui.ErrorWriter.String(), want)
	}
}

// An interrupt while following stops the run's unfinished execution and
// exits 2; following an execution's output, it stops following only.
func TestFollow_Interrupted(t *testing.T) {
	running := `{"DispatchID":"d-1","Job":"ci","State":"running","Executions":[{"ID":"e-1","Task":"build","State":"running"}]}`

	ctx, cancel := context.WithCancel(t.Context())

	var reads, cancels atomic.Int32

	client := stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			cancels.Add(1)
		} else {
			reads.Add(1)
		}

		_, _ = io.WriteString(w, running)
	})

	// The interrupt arrives while the poll waits for its second read
	go func() {
		for reads.Load() == 0 {
			time.Sleep(time.Millisecond)
		}

		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	ui := cli.NewMockUi()

	code := (&Meta{Ui: ui}).follow(ctx, client, &api.DispatchResponse{DispatchID: "d-1", Job: "ci"}, true)
	if code != ExitNoCapacity || cancels.Load() != 1 {
		t.Errorf("exit %d after %d cancels, want 2 after one", code, cancels.Load())
	}

	logs, stop := context.WithCancel(t.Context())
	stop()

	if code := (&ExecutionLogsCommand{Meta: &Meta{Ui: ui}}).follow(logs, client, "e-1"); code != ExitSuccess {
		t.Errorf("execution logs -f exit %d after an interrupt, want 0", code)
	}
}

// -------------------------------------------------------------------------
// VERDICT
// -------------------------------------------------------------------------

// The verdict line ends with the platform's reason when it gave one, and is
// printed once however often the execution is seen.
func TestFollower_Result(t *testing.T) {
	const evicted = "Evicted: Pod ephemeral local storage usage exceeds the total limit of containers 400M."

	tests := []struct {
		name   string
		reason string
		want   string
	}{
		{"no reason", "", "==> make failed (exit 137) on ibm-code-engine in 35s\n"},
		{"with a reason", evicted, "==> make failed (exit 137) on ibm-code-engine in 35s: " + evicted + "\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ui := cli.NewMockUi()
			f := (&Meta{Ui: ui}).newFollower(nil, true)

			e := &api.Execution{
				ID: "exec-1", Task: "make", Provider: "ibm-code-engine", State: executionFailed,
				HasResult: true, ExitCode: new(137), Duration: 35 * time.Second, Reason: tc.reason,
			}

			f.result(t.Context(), e)
			f.result(t.Context(), e)

			if got := ui.ErrorWriter.String(); got != tc.want {
				t.Errorf("printed %q, want %q", got, tc.want)
			}
		})
	}
}
