// -------------------------------------------------------------------------------
// Log Streaming Tests
//
// Author: Alex Freidah
//
// The decoder is the part worth testing: entries have to reach the writer as
// each chunk arrives rather than when the response ends, or this is a slow poll
// wearing a stream's interface.
// -------------------------------------------------------------------------------

package gcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

func TestCopyTailWritesEntries(t *testing.T) {
	t.Parallel()

	body := `[{"entries":[{"textPayload":"first"},{"textPayload":"second"}]}]`

	var out bytes.Buffer

	if err := copyTail(strings.NewReader(body), &out); err != nil {
		t.Fatalf("copyTail failed: %v", err)
	}

	if want := "first\nsecond\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// Cloud Logging interleaves structural records with the container's output.
func TestCopyTailSkipsEmptyPayloads(t *testing.T) {
	t.Parallel()

	body := `[{"entries":[{"textPayload":""},{"textPayload":"real"}]}]`

	var out bytes.Buffer

	if err := copyTail(strings.NewReader(body), &out); err != nil {
		t.Fatalf("copyTail failed: %v", err)
	}

	if want := "real\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// A stream that ends is not a stream that broke.
func TestCopyTailTreatsTruncationAsTheEnd(t *testing.T) {
	t.Parallel()

	// An array that never closes, which is what a cut connection leaves.
	body := `[{"entries":[{"textPayload":"partial"}]}`

	var out bytes.Buffer

	if err := copyTail(strings.NewReader(body), &out); err != nil {
		t.Errorf("a truncated stream was reported as an error: %v", err)
	}

	if !strings.Contains(out.String(), "partial") {
		t.Errorf("output before the cut was lost: %q", out.String())
	}
}

func TestCopyTailOnAnEmptyStream(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	if err := copyTail(strings.NewReader(`[]`), &out); err != nil {
		t.Fatalf("copyTail failed: %v", err)
	}

	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing", out.String())
	}
}

// The point of a stream: a chunk reaches the writer before the response ends.
func TestCopyTailIsIncremental(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()
	defer pw.Close()

	var (
		mu  sync.Mutex
		out bytes.Buffer
	)

	done := make(chan error, 1)

	go func() {
		done <- copyTail(pr, writerFunc(func(p []byte) (int, error) {
			mu.Lock()
			defer mu.Unlock()

			return out.Write(p)
		}))
	}()

	// One chunk, and the response deliberately left open.
	_, _ = io.WriteString(pw, `[{"entries":[{"textPayload":"early"}]}`)

	deadline := time.After(2 * time.Second)

	for {
		mu.Lock()
		got := out.String()
		mu.Unlock()

		if strings.Contains(got, "early") {
			break
		}

		select {
		case <-deadline:
			t.Fatal("nothing was written while the stream was still open")

		case <-time.After(5 * time.Millisecond):
		}
	}

	pw.Close()

	if err := <-done; err != nil {
		t.Errorf("copyTail failed: %v", err)
	}
}

// writerFunc adapts a function to io.Writer.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// -------------------------------------------------------------------------
// THE REQUEST
// -------------------------------------------------------------------------

// A streaming method's body is a stream of requests, so a bare object is
// rejected by Google as "Invalid value (Object)".
func TestStreamLogsSendsAnArrayBody(t *testing.T) {
	t.Parallel()

	var body []byte

	server := newRecordingServer(t, &body,
		`[{"entries":[{"textPayload":"streamed"}]}]`)

	p := &Provider{
		name:    "gcp",
		cfg:     &Config{Project: "test-project", Region: "us-central1"},
		http:    server.Client(),
		logsURL: server.URL,
	}

	var out bytes.Buffer

	if err := p.StreamLogs(t.Context(), newID(t), &out); err != nil {
		t.Fatalf("StreamLogs failed: %v", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body was not a JSON array: %s", body)
	}

	if len(decoded) != 1 {
		t.Fatalf("sent %d requests, want 1", len(decoded))
	}

	if got := decoded[0]["bufferWindow"]; got != bufferWindow {
		t.Errorf("bufferWindow = %v, want %s", got, bufferWindow)
	}

	if filter, _ := decoded[0]["filter"].(string); !strings.Contains(filter, "vagabond-") {
		t.Errorf("filter does not name the job: %q", filter)
	}

	if out.String() != "streamed\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestStreamLogsClassifiesAFailedOpen(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	// Nothing registered for entries:tail, so it 404s.
	_ = g

	if err := p.StreamLogs(t.Context(), newID(t), io.Discard); err == nil {
		t.Fatal("a failed open was reported as success")
	}
}

// newRecordingServer captures the request body and returns a canned stream.
func newRecordingServer(t *testing.T, body *[]byte, response string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			captured, _ := io.ReadAll(r.Body)
			*body = captured

			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, response)
		}))

	t.Cleanup(server.Close)

	return server
}
