// -------------------------------------------------------------------------------
// Follower Tests
//
// Author: Alex Freidah
//
// The parts of following a run that a real server cannot be made to produce on
// demand: a response that ends or breaks before the output is whole, output
// missing its front, cut by the provider or unreadable, an execution that ends
// with no result, a platform's reason for ending one, and a server that stops
// answering mid-run. A stub answers the routes.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"cmp"
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

// logsAnswer is what a stub's logs route sends: a status, where the body
// begins in the output, and the body.
type logsAnswer struct {
	status int
	start  int64
	body   string
}

// stubLogs is a client for a server whose logs route answers with answer,
// given the offset asked for, 0 when none was.
func stubLogs(t *testing.T, answer func(offset int64) logsAnswer) *api.Client {
	t.Helper()

	return stubServer(t, func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.ParseInt(cmp.Or(r.URL.Query().Get("offset"), "0"), 10, 64)
		a := answer(offset)

		if a.status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
		}

		w.Header().Set(api.LogsStartHeader, strconv.FormatInt(max(a.start, offset), 10))
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	})
}

// stored answers from the output a server kept, from byte start on, placed
// where each read asks as the server places it.
func stored(start int64, kept string) func(offset int64) logsAnswer {
	return func(offset int64) logsAnswer {
		from := max(offset, start)

		return logsAnswer{status: http.StatusOK, start: from, body: kept[min(from-start, int64(len(kept))):]}
	}
}

// awaitPrinted waits for out to print something, failing the test past five
// seconds.
func awaitPrinted(t *testing.T, out *output) {
	t.Helper()

	for deadline := time.Now().Add(5 * time.Second); out.position() == 0; {
		if time.Now().After(deadline) {
			t.Fatal("nothing was printed")
		}

		time.Sleep(time.Millisecond)
	}
}

// quickReconnects shortens a follow's wait between responses until the test
// ends.
func quickReconnects(t *testing.T) {
	t.Helper()

	saved := reconnectDelay
	reconnectDelay = time.Millisecond

	t.Cleanup(func() { reconnectDelay = saved })
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

// A response that ends while the execution runs is asked for again from where
// the output got to, at once once the execution ends, and a follow ends with a
// response asked for after that, printing every byte once. Output dropped
// between two responses is warned of where it was skipped.
func TestFollower_AsksAgainFromWhereItGotTo(t *testing.T) {
	tests := []struct {
		name     string
		second   logsAnswer // the answer from offset 6 on
		wantOut  string
		wantWarn string
	}{
		{"the rest", logsAnswer{http.StatusOK, 6, "second\n"}, "first\nsecond\n", ""},
		{"past a dropped front", logsAnswer{http.StatusOK, 100, "tail\n"}, "first\ntail\n", "94 bytes of output were not kept"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := stubLogs(t, func(offset int64) logsAnswer {
				if offset == 0 {
					return logsAnswer{http.StatusOK, 0, "first\n"}
				}

				return tc.second
			})

			var stdout bytes.Buffer

			ui := cli.NewMockUi()
			f := (&Meta{Ui: ui, Stdout: &stdout}).newFollower(client, false)

			f.watch(t.Context(), &api.Execution{ID: "exec-1", State: "running"})
			awaitPrinted(t, f.live["exec-1"].out)

			f.rest(t.Context(), &api.Execution{ID: "exec-1"})

			warned := ui.ErrorWriter.String()

			if stdout.String() != tc.wantOut || (tc.wantWarn == "") != (warned == "") || !strings.Contains(warned, tc.wantWarn) {
				t.Errorf("stdout %q, warned %q; want %q and %q", stdout.String(), warned, tc.wantOut, tc.wantWarn)
			}
		})
	}
}

// An ended execution's output is followed from the start when it was not
// followed while it ran, ended on a line of its own, and what is not there to
// print is warned of.
func TestFollower_Rest(t *testing.T) {
	quickReconnects(t)

	tests := []struct {
		name      string
		answer    func(offset int64) logsAnswer
		truncated bool
		wantOut   string
		wantWarn  string
	}{
		{"whole", stored(0, "all\n"), false, "all\n", ""},
		{"output ends mid-line", stored(0, "partial"), false, "partial\n", ""},
		{"front dropped", stored(100, "tail\n"), false, "tail\n", "100 bytes of output were not kept"},
		{"provider gave part", stored(0, "part\n"), true, "part\n", "The provider gave only part of the output."},
		{
			"unreadable", func(int64) logsAnswer { return logsAnswer{http.StatusInternalServerError, 0, `{"Error":"boom"}`} },
			false, "", "Could not read the output",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer

			ui := cli.NewMockUi()
			f := (&Meta{Ui: ui, Stdout: &stdout}).newFollower(stubLogs(t, tc.answer), false)

			f.rest(t.Context(), &api.Execution{ID: "exec-1", Ended: new(time.Now()), LogsTruncated: tc.truncated})

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

// The first state line of each execution names it, and later ones do not, so
// a retry is named on its own first line.
func TestFollower_ReportNamesEachExecutionOnce(t *testing.T) {
	ui := cli.NewMockUi()
	f := (&Meta{Ui: ui}).newFollower(nil, true)

	first := api.Execution{ID: "exec-1", Task: "make", Provider: "fn", State: executionPending, Attempt: 1}

	f.report(t.Context(), &api.Dispatch{Executions: []api.Execution{first}})

	first.State = "running"
	f.report(t.Context(), &api.Dispatch{Executions: []api.Execution{first}})

	retry := api.Execution{ID: "exec-2", Task: "make", Provider: "box", State: executionPending, Attempt: 2}
	f.report(t.Context(), &api.Dispatch{Executions: []api.Execution{first, retry}})

	want := "==> make pending on fn (execution exec-1)\n" +
		"==> make running on fn\n" +
		"==> make pending on box (attempt 2) (execution exec-2)\n"

	if got := ui.ErrorWriter.String(); got != want {
		t.Errorf("printed:\n%s\nwant:\n%s", got, want)
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

	want := "Dispatch d-1 may still be running. Check it with vagabond dispatch status d-1."
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

// The verdict line ends with the platform's reason when it gave one, names the
// execution when no line has yet, and is printed once however often the
// execution is seen.
func TestFollower_Result(t *testing.T) {
	const evicted = "Evicted: Pod ephemeral local storage usage exceeds the total limit of containers 400M."

	tests := []struct {
		name   string
		reason string
		seen   bool
		want   string
	}{
		{"no reason", "", true, "==> make failed (exit 137) on ibm-code-engine in 35s\n"},
		{"with a reason", evicted, true, "==> make failed (exit 137) on ibm-code-engine in 35s: " + evicted + "\n"},
		{"first seen ended", "", false, "==> make failed (exit 137) on ibm-code-engine in 35s (execution exec-1)\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ui := cli.NewMockUi()
			f := (&Meta{Ui: ui}).newFollower(nil, true)

			e := &api.Execution{
				ID: "exec-1", Task: "make", Provider: "ibm-code-engine", State: executionFailed,
				HasResult: true, ExitCode: new(137), Duration: 35 * time.Second, Reason: tc.reason,
			}

			if tc.seen {
				f.states[e.ID] = "running"
			}

			f.result(t.Context(), e)
			f.result(t.Context(), e)

			if got := ui.ErrorWriter.String(); got != tc.want {
				t.Errorf("printed %q, want %q", got, tc.want)
			}
		})
	}
}
