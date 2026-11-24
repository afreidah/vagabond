// -------------------------------------------------------------------------------
// Capturing Entries Tests
//
// Author: Alex Freidah
//
// A capture has to hold every line once however its pieces fall: the catch-up
// before the tail, the tail's copies of what was listed, a line logged too late
// for the tail, a log another capture began, and a line printed twice. The
// decoder has to hand entries over as each chunk arrives, and say why a tail
// ended, since a tail never ends of its own accord.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
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

// testFilter is the filter every list and tail here asks for.
const testFilter = `resource.type="cloud_run_job" resource.labels.job_name="vagabond-test"`

// fakeLogging answers entries:list with pages in turn, the last repeating, and
// entries:tail with one canned stream, held open until the request ends when
// hold is set. Safe for the concurrent requests a capture makes.
type fakeLogging struct {
	mu         sync.Mutex
	pages      []any
	tail       string
	hold       bool
	listStatus int // when set, entries:list is refused with it
	tailStatus int // when set, entries:tail is refused with it
	tailBody   []byte
	lists      []map[string]any // the body of each entries:list request
}

// ServeHTTP answers the two routes and records what they were sent.
func (f *fakeLogging) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/v2/entries:tail":
		f.serveTail(w, r, body)
	case "/v2/entries:list":
		f.serveList(w, body)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// serveTail writes the canned tail, then holds the response open when asked,
// outside the lock so the lists a capture makes meanwhile are answered.
func (f *fakeLogging) serveTail(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	f.tailBody = body
	status, tail, hold := f.tailStatus, f.tail, f.hold
	f.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"refused"}}`)

		return
	}

	_, _ = io.WriteString(w, tail)
	_ = http.NewResponseController(w).Flush()

	if hold {
		<-r.Context().Done()
	}
}

// serveList answers with the next page, recording the request.
func (f *fakeLogging) serveList(w http.ResponseWriter, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var request map[string]any
	_ = json.Unmarshal(body, &request)

	f.lists = append(f.lists, request)

	if f.listStatus != 0 {
		w.WriteHeader(f.listStatus)
		_, _ = io.WriteString(w, `{"error":{"message":"refused"}}`)

		return
	}

	_ = json.NewEncoder(w).Encode(f.pages[min(len(f.lists), len(f.pages))-1])
}

// listed is how many list requests f has answered.
func (f *fakeLogging) listed() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.lists)
}

// tailed is the body of the tail request f was sent, nil when none was.
func (f *fakeLogging) tailed() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.tailBody
}

// newLogging starts f and an API reading logs from it, its requests unsigned
// since the fake needs no auth, and its captures not waiting to settle.
func newLogging(t *testing.T, f *fakeLogging) *API {
	t.Helper()

	server := httptest.NewServer(f)
	t.Cleanup(server.Close)

	client, diags := plugin.NewCredentialed(t.Context(), "gcp", nil,
		func(context.Context, []byte) (*http.Client, error) { return server.Client(), nil })
	if diags.HasErrors() {
		t.Fatalf("NewCredentialed() = %s", diags.Error())
	}

	return &API{Credentialed: client, Config: &Config{Project: "test-project"}, LogsURL: server.URL, name: "gcp"}
}

// keyed is one entry as Cloud Logging returns it, with its identity.
func keyed(text, insertID string) map[string]any {
	return map[string]any{"textPayload": text, "timestamp": "2026-11-12T10:00:0" + insertID + "Z", "insertId": insertID}
}

// tailOf renders a tail of one response carrying entries, its array left open
// as a live tail's is.
func tailOf(t *testing.T, entries ...any) string {
	t.Helper()

	encoded, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatalf("encoding the tail: %v", err)
	}

	return "[" + string(encoded)
}

// ended is an ended that has closed, for a capture of a finished run.
func ended() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)

	return ch
}

// holding is a memory log already holding text.
func holding(text string) *plugin.MemoryLog {
	log := &plugin.MemoryLog{}
	_, _ = log.Write([]byte(text))

	return log
}

// brokenLog is a log every write to fails, as one whose lease moved does.
type brokenLog struct {
	plugin.MemoryLog
}

// Write fails.
func (*brokenLog) Write([]byte) (int, error) { return 0, errors.New("lease moved") }

// -------------------------------------------------------------------------
// CAPTURING
// -------------------------------------------------------------------------

// What was logged before the tail opened comes first, an entry both the list
// and the tail carry is written once, and the last catch-up appends what was
// logged too late for the tail.
func TestCaptureEntries_CatchesUpTailsAndSettles(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{
		pages: []any{
			page("", keyed("one", "1"), keyed("two", "2")),
			page("", keyed("one", "1"), keyed("two", "2"), keyed("three", "3"), keyed("late", "5")),
		},
		tail: tailOf(t, keyed("two", "2"), entry(""), keyed("three", "3")),
		hold: true,
	}

	log, end := &plugin.MemoryLog{}, make(chan struct{})
	done := make(chan error, 1)

	go func() { done <- newLogging(t, f).CaptureEntries(t.Context(), testFilter, log, end) }()

	// The tail's last line is in before the run ends
	for deadline := time.Now().Add(5 * time.Second); !strings.HasSuffix(log.String(), "three\n"); {
		if time.Now().After(deadline) {
			t.Fatalf("the tail's lines never arrived: %q", log.String())
		}

		time.Sleep(time.Millisecond)
	}

	close(end)

	if err := <-done; err != nil || log.String() != "one\ntwo\nthree\nlate\n" {
		t.Errorf("CaptureEntries() = %q, %v; want every line once", log.String(), err)
	}
}

// A capture of a finished run appends only what its log lacks, counting a
// line held twice as two, so a capture another server began is finished.
func TestCaptureEntries_ResumesFromTheLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		held    string
		entries []any
		want    string
	}{
		{
			name: "the rest after what is held", held: "one\ntwo\n",
			entries: []any{keyed("one", "1"), keyed("two", "2"), keyed("three", "3")}, want: "one\ntwo\nthree\n",
		},
		{
			name: "a line printed twice", held: "ok\n",
			entries: []any{keyed("ok", "1"), keyed("ok", "2")}, want: "ok\nok\n",
		},
		{
			name: "an entry of two lines, held", held: "a\nb\n",
			entries: []any{keyed("a\nb", "1")}, want: "a\nb\n",
		},
		{
			name: "a log cut mid-line", held: "par",
			entries: []any{keyed("partial", "1")}, want: "par\npartial\n",
		},
		{name: "nothing held", held: "", entries: []any{keyed("one", "1")}, want: "one\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			log := holding(tc.held)
			f := &fakeLogging{pages: []any{page("", tc.entries...)}}

			if err := newLogging(t, f).CaptureEntries(t.Context(), testFilter, log, ended()); err != nil || log.String() != tc.want {
				t.Errorf("CaptureEntries() = %q, %v; want %q", log.String(), err, tc.want)
			}

			if f.listed() != 1 || f.tailed() != nil {
				t.Errorf("made %d lists and tailed %t, want one list and no tail", f.listed(), f.tailed() != nil)
			}
		})
	}
}

// A tail that ends before the run does is an error saying why, after the
// catch-up, so the capture is made again.
func TestCaptureEntries_ReportsAnEndedTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, tail, want string
	}{
		{name: "ended by Google", tail: `[{"error":{"status":"UNAVAILABLE","message":"try again"}}]`, want: "UNAVAILABLE try again"},
		{name: "ended without a word", tail: `[]`, want: errTailEnded.Error()},
		{name: "cut", tail: `[{"entries":[`, want: "reading the tail"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			log := &plugin.MemoryLog{}
			f := &fakeLogging{pages: []any{page("", keyed("one", "1"))}, tail: tc.tail}

			err := newLogging(t, f).CaptureEntries(t.Context(), testFilter, log, make(chan struct{}))
			if err == nil || !strings.Contains(err.Error(), tc.want) || classOf(err) != plugin.ClassInfrastructure {
				t.Errorf("CaptureEntries() = %v, want an Infrastructure error saying %q", err, tc.want)
			}

			if log.String() != "one\n" {
				t.Errorf("captured %q, want the catch-up's line", log.String())
			}
		})
	}
}

// The tail is a streaming method's body, a JSON array of requests, since a
// bare object is rejected by Google as "Invalid value (Object)".
func TestCaptureEntries_SendsAnArrayBody(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{pages: []any{page("")}, tail: `[]`}

	_ = newLogging(t, f).CaptureEntries(t.Context(), testFilter, &plugin.MemoryLog{}, make(chan struct{}))

	var decoded []map[string]any
	if err := json.Unmarshal(f.tailed(), &decoded); err != nil || len(decoded) != 1 {
		t.Fatalf("tail body = %s, want an array of one request", f.tailed())
	}

	if got := decoded[0]["bufferWindow"]; got != bufferWindow {
		t.Errorf("bufferWindow = %v, want %s", got, bufferWindow)
	}

	if got := decoded[0]["filter"]; got != testFilter {
		t.Errorf("filter = %v, want %s", got, testFilter)
	}
}

// A tail Google refuses to open is reported, before anything is listed.
func TestCaptureEntries_ClassifiesAFailedOpen(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{tailStatus: http.StatusForbidden}

	err := newLogging(t, f).CaptureEntries(t.Context(), testFilter, &plugin.MemoryLog{}, make(chan struct{}))
	if !errors.Is(err, plugin.ErrUnauthorized) {
		t.Fatalf("CaptureEntries() = %v, want the refusal", err)
	}

	if f.listed() != 0 {
		t.Errorf("listed %d pages, want only the tail opened", f.listed())
	}
}

// A catch-up that cannot list, or stops at the page cap, is the platform's.
func TestCaptureEntries_ReportsAFailedCatchUp(t *testing.T) {
	t.Parallel()

	refused := &fakeLogging{listStatus: http.StatusTooManyRequests}

	err := newLogging(t, refused).CaptureEntries(t.Context(), testFilter, &plugin.MemoryLog{}, ended())
	if classOf(err) != plugin.ClassInfrastructure {
		t.Errorf("CaptureEntries() with the list refused = %v, want an Infrastructure error", err)
	}

	endless := &fakeLogging{pages: []any{page("more")}}

	err = newLogging(t, endless).CaptureEntries(t.Context(), testFilter, &plugin.MemoryLog{}, ended())
	if !errors.Is(err, errPageCap) || classOf(err) != plugin.ClassInfrastructure {
		t.Errorf("CaptureEntries() at the page cap = %v, want an Infrastructure error wrapping errPageCap", err)
	}
}

// A write that fails is the log's, so it is Internal, in the catch-up and in
// the tail.
func TestCaptureEntries_ReportsAFailedWrite(t *testing.T) {
	t.Parallel()

	for name, f := range map[string]*fakeLogging{
		"catch-up": {pages: []any{page("", keyed("one", "1"))}, tail: `[]`},
		"tail":     {pages: []any{page("")}, tail: tailOf(t, keyed("one", "1")), hold: true},
	} {
		err := newLogging(t, f).CaptureEntries(t.Context(), testFilter, &brokenLog{}, make(chan struct{}))
		if classOf(err) != plugin.ClassInternal {
			t.Errorf("%s: CaptureEntries() = %v, want an Internal error", name, err)
		}
	}
}

// A capture ends with its context, waiting to settle or tailing.
func TestCaptureEntries_EndsWithItsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	a := newLogging(t, &fakeLogging{pages: []any{page("")}})
	a.Settle = time.Hour

	if err := a.CaptureEntries(ctx, testFilter, &plugin.MemoryLog{}, ended()); !errors.Is(err, context.Canceled) {
		t.Errorf("CaptureEntries() = %v, want context.Canceled", err)
	}
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// collect decodes body and returns each entry's text, in order, and why the
// decoding ended.
func collect(body io.Reader) ([]string, error) {
	var texts []string

	err := decodeTail(body, func(entry *logEntry) error {
		texts = append(texts, entry.TextPayload)

		return nil
	})

	return texts, err
}

// Every entry of every response is handed over, in order, and the end of the
// array is reported, since a tail never ends of its own accord.
func TestDecodeTail_HandsOverEntries(t *testing.T) {
	t.Parallel()

	body := `[{"entries":[{"textPayload":"first"},{"textPayload":"second"}]},{"entries":[{"textPayload":"third"}]}]`

	got, err := collect(strings.NewReader(body))
	if strings.Join(got, ",") != "first,second,third" || !errors.Is(err, errTailEnded) {
		t.Errorf("collect() = %q, %v; want every entry, then errTailEnded", got, err)
	}
}

// Google's closing error, and a cut, end the decoding with what came before
// handed over.
func TestDecodeTail_ReportsWhyItEnded(t *testing.T) {
	t.Parallel()

	got, err := collect(strings.NewReader(`[{"entries":[{"textPayload":"partial"}]},{"error":{"status":"INTERNAL","message":"gone"}}]`))
	if len(got) != 1 || err == nil || !strings.Contains(err.Error(), "INTERNAL gone") {
		t.Errorf("collect() = %q, %v; want the entry, then Google's error", got, err)
	}

	got, err = collect(strings.NewReader(`[{"entries":[{"textPayload":"partial"}]}`))
	if len(got) != 1 || classOf(err) != plugin.ClassInfrastructure {
		t.Errorf("collect() of a cut tail = %q, %v; want the entry, then an Infrastructure error", got, err)
	}

	if _, err := collect(strings.NewReader(``)); classOf(err) != plugin.ClassInfrastructure {
		t.Errorf("collect() of nothing = %v, want an Infrastructure error", err)
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

	if err := <-done; err == nil {
		t.Error("decodeTail() = nil once the stream was cut, want why")
	}
}
