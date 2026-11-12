// -------------------------------------------------------------------------------
// Log Streaming Tests
//
// Author: Alex Freidah
//
// The decoder has to hand entries over as each chunk arrives rather than when
// the response ends, or this is a slow poll wearing a stream's interface. The
// stream has to start at the first byte the task printed, whenever it opens:
// the backfill before the tail, nothing twice, and an offset placed in both.
// -------------------------------------------------------------------------------

package gcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// fakeLogging answers entries:list with pages in turn, the last repeating, and
// entries:tail with one canned stream. Safe for the concurrent requests a
// stream makes.
type fakeLogging struct {
	mu         sync.Mutex
	pages      []any
	tail       string
	listStatus int // when set, entries:list is refused with it
	tailBody   []byte
	lists      int
}

// ServeHTTP answers the two routes and records what they were sent.
func (f *fakeLogging) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	defer f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/v2/entries:tail":
		f.tailBody = body
		_, _ = io.WriteString(w, f.tail)

	case "/v2/entries:list":
		f.lists++

		if f.listStatus != 0 {
			w.WriteHeader(f.listStatus)
			_, _ = io.WriteString(w, `{"error":{"message":"refused"}}`)

			return
		}

		_ = json.NewEncoder(w).Encode(f.pages[min(f.lists, len(f.pages))-1])

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// newLogging starts f and a provider reading logs from it.
func newLogging(t *testing.T, f *fakeLogging) *Provider {
	t.Helper()

	server := httptest.NewServer(f)
	t.Cleanup(server.Close)

	p := &Provider{name: "gcp", cfg: &Config{Project: "test-project", Region: "us-central1"}, logsURL: server.URL}
	p.http.Store(server.Client())

	return p
}

// keyed is one entry as Cloud Logging returns it, with its identity.
func keyed(text, insertID string) map[string]any {
	return map[string]any{"textPayload": text, "timestamp": "2026-11-12T10:00:0" + insertID + "Z", "insertId": insertID}
}

// tailOf renders a tail stream of one response carrying entries.
func tailOf(t *testing.T, entries ...any) string {
	t.Helper()

	encoded, err := json.Marshal([]any{map[string]any{"entries": entries}})
	if err != nil {
		t.Fatalf("encoding the tail: %v", err)
	}

	return string(encoded)
}

// stream runs StreamLogs from offset and returns what it wrote.
func stream(t *testing.T, p *Provider, offset int64) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := p.StreamLogs(t.Context(), newID(t), offset, &out)

	return out.String(), err
}

// writerFunc adapts a function to io.Writer.
type writerFunc func(p []byte) (int, error)

// Write calls the function.
func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// -------------------------------------------------------------------------
// STREAMING
// -------------------------------------------------------------------------

// What was logged before the stream opened comes first, and an entry both the
// list and the tail carry is written once.
func TestStreamLogs_BackfillsThenTails(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{
		pages: []any{page("", keyed("one", "1"), keyed("two", "2"))},
		tail:  tailOf(t, keyed("two", "2"), entry(""), keyed("three", "3")),
	}

	got, err := stream(t, newLogging(t, f), 0)
	if err != nil || got != "one\ntwo\nthree\n" {
		t.Errorf("StreamLogs() = %q, %v; want every line once, in order", got, err)
	}
}

// A stream reopened at an offset skips what the caller has, across the
// backfill and into the tail.
func TestStreamLogs_ResumesFromAnOffset(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{
		pages: []any{page("", keyed("one", "1"), keyed("two", "2"))},
		tail:  tailOf(t, keyed("three", "3")),
	}
	p := newLogging(t, f)

	for offset, want := range map[int64]string{
		int64(len("one\n")):             "two\nthree\n",
		int64(len("one\ntwo\nth")):      "ree\n",
		int64(len("one\ntwo\nthree\n")): "",
	} {
		if got, err := stream(t, p, offset); err != nil || got != want {
			t.Errorf("StreamLogs() from %d = %q, %v; want %q", offset, got, err, want)
		}
	}
}

// The tail is a streaming method's body, a JSON array of requests, since a
// bare object is rejected by Google as "Invalid value (Object)".
func TestStreamLogs_SendsAnArrayBody(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{pages: []any{page("")}, tail: `[]`}

	if _, err := stream(t, newLogging(t, f), 0); err != nil {
		t.Fatalf("StreamLogs() = %v", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(f.tailBody, &decoded); err != nil || len(decoded) != 1 {
		t.Fatalf("tail body = %s, want an array of one request", f.tailBody)
	}

	if got := decoded[0]["bufferWindow"]; got != bufferWindow {
		t.Errorf("bufferWindow = %v, want %s", got, bufferWindow)
	}

	if filter, _ := decoded[0]["filter"].(string); !strings.Contains(filter, "vagabond-") {
		t.Errorf("filter does not name the job: %q", filter)
	}
}

// A backfill that cannot be listed, or stops at the page cap, leaves the
// stream unplaced, which the caller learns as ErrNoResume.
func TestStreamLogs_UnplacedIsNoResume(t *testing.T) {
	t.Parallel()

	refused := &fakeLogging{listStatus: http.StatusTooManyRequests, tail: `[]`}

	if _, err := stream(t, newLogging(t, refused), 0); !errors.Is(err, plugin.ErrNoResume) {
		t.Errorf("StreamLogs() with the list refused = %v, want ErrNoResume", err)
	}

	endless := &fakeLogging{pages: []any{page("more")}, tail: `[]`}

	if _, err := stream(t, newLogging(t, endless), 0); !errors.Is(err, plugin.ErrNoResume) || !errors.Is(err, errPageCap) {
		t.Errorf("StreamLogs() at the page cap = %v, want ErrNoResume", err)
	}
}

// A tail Google refuses to open is reported, before anything is listed.
func TestStreamLogs_ClassifiesAFailedOpen(t *testing.T) {
	t.Parallel()

	// Nothing registered for entries:tail, so it 404s.
	g, p := newFakeGoogle(t)

	if _, err := stream(t, p, 0); err == nil || errors.Is(err, plugin.ErrNoResume) {
		t.Fatalf("StreamLogs() = %v, want the refusal", err)
	}

	if len(g.requests) != 1 {
		t.Errorf("made %d requests, want only the tail", len(g.requests))
	}
}

// A write that fails is the caller's, in the backfill and in the tail.
func TestStreamLogs_ReportsAFailedWrite(t *testing.T) {
	t.Parallel()

	gone := writerFunc(func([]byte) (int, error) { return 0, errors.New("client gone") })

	for name, f := range map[string]*fakeLogging{
		"backfill": {pages: []any{page("", keyed("one", "1"))}, tail: `[]`},
		"tail":     {pages: []any{page("")}, tail: tailOf(t, keyed("one", "1"))},
	} {
		err := newLogging(t, f).StreamLogs(t.Context(), newID(t), 0, gone)
		if classOf(err) != plugin.ClassInternal || errors.Is(err, plugin.ErrNoResume) {
			t.Errorf("%s: StreamLogs() = %v, want an Internal error", name, err)
		}
	}
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// collect decodes body and returns each entry's text, in order.
func collect(t *testing.T, body io.Reader) []string {
	t.Helper()

	var texts []string

	err := decodeTail(body, func(entry *logEntry) error {
		texts = append(texts, entry.TextPayload)

		return nil
	})
	if err != nil {
		t.Fatalf("decodeTail() = %v", err)
	}

	return texts
}

// Every entry of every response is handed over, in order.
func TestDecodeTail_HandsOverEntries(t *testing.T) {
	t.Parallel()

	body := `[{"entries":[{"textPayload":"first"},{"textPayload":"second"}]},{"entries":[{"textPayload":"third"}]}]`

	if got := strings.Join(collect(t, strings.NewReader(body)), ","); got != "first,second,third" {
		t.Errorf("entries = %q", got)
	}
}

// A stream that ends is not a stream that broke, and an empty one hands over
// nothing.
func TestDecodeTail_TreatsTruncationAsTheEnd(t *testing.T) {
	t.Parallel()

	// An array that never closes, which is what a cut connection leaves.
	if got := collect(t, strings.NewReader(`[{"entries":[{"textPayload":"partial"}]}`)); len(got) != 1 {
		t.Errorf("entries before the cut = %q", got)
	}

	if got := collect(t, strings.NewReader(`[]`)); len(got) != 0 {
		t.Errorf("entries of an empty stream = %q", got)
	}
}

// What each returns ends the decoding.
func TestDecodeTail_StopsWhenEachFails(t *testing.T) {
	t.Parallel()

	stop := errors.New("stop")
	body := `[{"entries":[{"textPayload":"first"},{"textPayload":"second"}]}]`

	calls := 0

	err := decodeTail(strings.NewReader(body), func(*logEntry) error {
		calls++

		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("decodeTail() = %v after %d calls, want stop after 1", err, calls)
	}
}

// The point of a stream: an entry is handed over before the response ends.
func TestDecodeTail_IsIncremental(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()
	defer pw.Close()

	arrived := make(chan string, 1)
	done := make(chan error, 1)

	go func() {
		done <- decodeTail(pr, func(entry *logEntry) error {
			arrived <- entry.TextPayload

			return nil
		})
	}()

	// One chunk, and the response deliberately left open.
	_, _ = io.WriteString(pw, `[{"entries":[{"textPayload":"early"}]}`)

	select {
	case got := <-arrived:
		if got != "early" {
			t.Errorf("handed over %q, want early", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was handed over while the stream was still open")
	}

	pw.Close()

	if err := <-done; err != nil {
		t.Errorf("decodeTail() = %v", err)
	}
}
