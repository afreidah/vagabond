// -------------------------------------------------------------------------------
// Follower Tests
//
// Author: Alex Freidah
//
// The parts of following a run that a real server cannot be made to produce on
// demand: a stream that cannot resume, a truncated or unreadable stored copy,
// an execution that ends with no result, and a platform's reason for ending
// one. A stub answers the logs route.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, body := answer(r.URL.Query().Get("offset"))

		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
		}

		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	client, err := api.NewClient(server.URL, nil)
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}

	return client
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

	if stdout.String() != "first\n" || !l.broke {
		t.Errorf("stdout %q, broke %v; want the first stream's output and a broken follow", stdout.String(), l.broke)
	}
}

// The stored copy finishes what did not stream, from the bytes printed, and
// says when the output cannot be shown whole.
func TestFollower_Rest(t *testing.T) {
	tests := []struct {
		name      string
		streamed  string // what a follow already printed, empty for none
		broke     bool
		truncated bool
		status    int
		stored    string
		wantOut   string
		wantWarn  string
	}{
		{"nothing streamed", "", false, false, http.StatusOK, "all\n", "all\n", ""},
		{"output ends mid-line", "", false, false, http.StatusOK, "partial", "partial\n", ""},
		{"the rest after a stream", "first\n", false, false, http.StatusOK, "rest\n", "first\nrest\n", ""},
		{"everything streamed", "all\n", false, false, http.StatusOK, "", "all\n", ""},
		{"truncated, nothing streamed", "", false, true, http.StatusOK, "tail\n", "tail\n", "Output was truncated."},
		{"truncated after a broken stream", "first\n", true, true, http.StatusOK, "", "first\n", "Live output was interrupted"},
		{"stored copy unreadable", "", false, false, http.StatusInternalServerError, `{"Error":"boom"}`, "", "Could not read the output"},
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

				l := &live{out: &output{mu: &f.stdout, w: &stdout}, end: func() {}, done: done, broke: tc.broke}
				_, _ = l.out.Write([]byte(tc.streamed))
				f.live["exec-1"] = l
			}

			f.rest(t.Context(), &api.Execution{ID: "exec-1", LogsTruncated: tc.truncated})

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
