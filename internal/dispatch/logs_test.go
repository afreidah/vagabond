// -------------------------------------------------------------------------------
// Capturing Output Tests
//
// Author: Alex Freidah
//
// Where an execution's output ends up: copied from a provider that holds it
// while the run goes, the call made again when it returns early, or taken from
// the answer that carried it; cut into chunks with the front dropped past the
// ceiling; resumed from what is stored; and marked truncated when it cannot
// all be had, never failing the run.
// -------------------------------------------------------------------------------

package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// captureFunc is what a capturing provider does on its nth call.
type captureFunc func(ctx context.Context, call int, id execution.ID, log execution.Log, ended <-chan struct{}) error

// capturingProvider holds its executions' output, as Cloud Run does, and
// captures it with capture. While hold is open, Status reports running.
type capturingProvider struct {
	scriptedProvider

	capture captureFunc
	hold    chan struct{}

	mu    sync.Mutex
	calls int
}

// CaptureLogs counts the call and does what capture does.
func (p *capturingProvider) CaptureLogs(
	ctx context.Context, id execution.ID, log execution.Log, ended <-chan struct{},
) error {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()

	return p.capture(ctx, call, id, log, ended)
}

// Status reports running while hold is open, then what the script says.
func (p *capturingProvider) Status(ctx context.Context, id execution.ID) (execution.Status, error) {
	if p.hold != nil && !plugin.Ended(p.hold) {
		return execution.Status{ID: id, State: execution.StateRunning}, nil
	}

	return p.scriptedProvider.Status(ctx, id)
}

// made is how many times CaptureLogs was called.
func (p *capturingProvider) made() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.calls
}

// capturing registers a capturing provider named "a" whose runs succeed at
// once.
func capturing(capture captureFunc) (*capturingProvider, *fakeRegistry) {
	p := &capturingProvider{
		name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded,
		capture: capture,
	}

	reg := newRegistry(&p.scriptedProvider)
	reg.providers["a"] = p

	return p, reg
}

// writes is a capture that writes what of text the log lacks, then waits for
// the run to be over.
func writes(text string) captureFunc {
	return func(ctx context.Context, _ int, _ execution.ID, log execution.Log, ended <-chan struct{}) error {
		if _, err := io.WriteString(plugin.SkipWriter(log, log.Len()), text); err != nil {
			return err
		}

		return awaitEnd(ctx, ended)
	}
}

// awaitEnd waits for the run to be over, or ctx to end.
func awaitEnd(ctx context.Context, ended <-chan struct{}) error {
	select {
	case <-ended:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stored returns what the registry's store holds of id's output.
func stored(t *testing.T, reg *fakeRegistry, id execution.ID) string {
	t.Helper()

	var out strings.Builder

	err := reg.executions.ReadLog(t.Context(), id, 0, func(_ int64, data []byte) error {
		out.Write(data)

		return nil
	})
	if err != nil {
		t.Fatalf("ReadLog() = %v", err)
	}

	return out.String()
}

// runOnce runs one container task and returns how it ended.
func runOnce(t *testing.T, d *Dispatcher) *TaskOutcome {
	t.Helper()

	outcome, err := d.runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask() = %v", err)
	}

	return outcome
}

// -------------------------------------------------------------------------
// CAPTURE
// -------------------------------------------------------------------------

// A provider that holds the output has it copied into the store while the run
// goes, and the record says when the copy was finished.
func TestCapture_StoresWhileItRuns(t *testing.T) {
	t.Parallel()

	var reg *fakeRegistry

	hold := make(chan struct{})

	p, reg := capturing(func(ctx context.Context, _ int, id execution.ID, log execution.Log, ended <-chan struct{}) error {
		if _, err := io.WriteString(log, "line one\n"); err != nil {
			return err
		}

		// The run goes on until the line is in the store
		for deadline := time.Now().Add(5 * time.Second); stored(t, reg, id) != "line one\n"; {
			if time.Now().After(deadline) {
				return errors.New("the line was never stored")
			}

			time.Sleep(10 * time.Millisecond)
		}

		close(hold)

		return awaitEnd(ctx, ended)
	})
	p.hold = hold

	outcome := runOnce(t, newDispatcher(t, reg))

	if got := stored(t, reg, outcome.ID); got != "line one\n" || p.made() != 1 {
		t.Errorf("stored %q after %d calls, want the output after one", got, p.made())
	}

	rec := record(t, reg, outcome.ID)
	if rec.LogsAt.IsZero() || rec.LogsStart != 0 || rec.LogsTruncated {
		t.Errorf("record says output at %s from %d, truncated %t; want captured whole",
			rec.LogsAt, rec.LogsStart, rec.LogsTruncated)
	}
}

// A call that returns early, cut short or refused as unauthorized, is made
// again from what the log holds, so nothing is written twice.
func TestCapture_CallsAgainFromTheLog(t *testing.T) {
	t.Parallel()

	tests := map[string]error{
		"cut short":    plugin.Infrastructure(errors.New("connection reset")),
		"unauthorized": plugin.ClassifyHTTP(http.StatusUnauthorized, 0, errors.New("expired token")),
	}

	for name, early := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, reg := capturing(endsEarlyOnce(early))
			reg.providers["a"] = &refreshingCapturer{capturingProvider: p}

			outcome := runOnce(t, newDispatcher(t, reg))

			if got := stored(t, reg, outcome.ID); got != "first\nsecond\n" || p.made() != 2 {
				t.Errorf("stored %q after %d calls, want every line once after two", got, p.made())
			}

			if rec := record(t, reg, outcome.ID); rec.LogsTruncated {
				t.Error("output captured whole on the second call was marked truncated")
			}
		})
	}
}

// endsEarlyOnce is a capture whose first call prints the first line and
// returns early, and whose later calls print what of both lines the log lacks.
func endsEarlyOnce(early error) captureFunc {
	return func(ctx context.Context, call int, _ execution.ID, log execution.Log, ended <-chan struct{}) error {
		if call == 1 {
			_, _ = io.WriteString(log, "first\n")

			return early
		}

		return writes("first\nsecond\n")(ctx, call, execution.ID{}, log, ended)
	}
}

// A provider that hands the output over with its answer has it stored from
// there, marked truncated when the answer said so.
func TestCapture_TakesTheAnswer(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded, logsTruncated: true}
	reg := newRegistry(p)
	outcome := runOnce(t, newDispatcher(t, reg))

	if got := stored(t, reg, outcome.ID); got != "scripted output\n" {
		t.Errorf("stored %q, want the answer's output", got)
	}

	if rec := record(t, reg, outcome.ID); !rec.LogsTruncated || rec.Result.Logs != nil {
		t.Errorf("record truncated %t with result logs %q; want truncated and none kept", rec.LogsTruncated, rec.Result.Logs)
	}
}

// Output past the ceiling loses its front a chunk at a time, and the record
// says where what is kept begins.
func TestCapture_DropsTheFrontPastTheCeiling(t *testing.T) {
	t.Parallel()

	head := strings.Repeat("a", 2*execution.LogChunk)
	_, reg := capturing(writes(head + "the end\n"))
	d := over(reg, WithSleeper(noWait), WithMaxLogBytes(execution.LogChunk))

	outcome := runOnce(t, d)

	if got := stored(t, reg, outcome.ID); got != "the end\n" {
		t.Errorf("stored %d bytes ending %q, want only the last chunk", len(got), got[max(0, len(got)-10):])
	}

	if rec := record(t, reg, outcome.ID); rec.LogsStart != 2*execution.LogChunk {
		t.Errorf("LogsStart = %d, want %d", rec.LogsStart, 2*execution.LogChunk)
	}
}

// Output a provider kept only the end of is stored where that end begins, and
// the record says so, what was written before dropped with it.
func TestCapture_PlacesOutputKeptOnlyFromItsEnd(t *testing.T) {
	t.Parallel()

	for name, before := range map[string]string{"nothing before": "", "part of a chunk before": "partial"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, reg := capturing(keptFromTheEnd(before))

			outcome := runOnce(t, newDispatcher(t, reg))

			if got := stored(t, reg, outcome.ID); got != "the end\n" {
				t.Errorf("stored %q, want only the end", got)
			}

			if rec := record(t, reg, outcome.ID); rec.LogsStart != 2*execution.LogChunk || rec.LogsTruncated {
				t.Errorf("LogsStart = %d, truncated %t; want %d, whole", rec.LogsStart, rec.LogsTruncated, 2*execution.LogChunk)
			}
		})
	}
}

// keptFromTheEnd is a capture that writes before, then places the output two
// chunks on, the provider having kept only its end, and writes that end.
func keptFromTheEnd(before string) captureFunc {
	return func(ctx context.Context, _ int, _ execution.ID, log execution.Log, ended <-chan struct{}) error {
		_, _ = io.WriteString(log, before)

		if err := log.StartsAt(2 * execution.LogChunk); err != nil {
			return err
		}

		_, _ = io.WriteString(log, "the end\n")

		return awaitEnd(ctx, ended)
	}
}

// Output that cannot all be had keeps what arrived, is marked truncated once
// the calls after the run's end have failed too, and leaves the verdict alone.
func TestCapture_GivesUpAfterTheEnd(t *testing.T) {
	t.Parallel()

	p, reg := capturing(func(_ context.Context, _ int, _ execution.ID, log execution.Log, _ <-chan struct{}) error {
		_, _ = io.WriteString(plugin.SkipWriter(log, log.Len()), "partial\n")

		return plugin.Infrastructure(errors.New("connection reset"))
	})

	outcome := runOnce(t, newDispatcher(t, reg))

	if !outcome.Succeeded() {
		t.Error("output that could not be had failed the run")
	}

	if got := stored(t, reg, outcome.ID); got != "partial\n" || p.made() < captureAttempts {
		t.Errorf("stored %q after %d calls, want what arrived after at least %d", got, p.made(), captureAttempts)
	}

	if rec := record(t, reg, outcome.ID); !rec.LogsTruncated || rec.LogsAt.IsZero() {
		t.Errorf("record truncated %t at %s, want truncated and captured", rec.LogsTruncated, rec.LogsAt)
	}
}

// A place that does not begin a chunk, or comes before what was written,
// cannot be stored, and the output is marked truncated.
func TestCapture_RefusesAPlaceItCannotStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		written string
		at      int64
	}{
		{"mid-chunk", "", execution.LogChunk + 1},
		{"before what was written", strings.Repeat("x", 2*execution.LogChunk), execution.LogChunk},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, reg := capturing(func(_ context.Context, _ int, _ execution.ID, log execution.Log, _ <-chan struct{}) error {
				if _, err := io.WriteString(plugin.SkipWriter(log, log.Len()), tt.written); err != nil {
					return err
				}

				return log.StartsAt(tt.at)
			})

			outcome := runOnce(t, newDispatcher(t, reg))

			if rec := record(t, reg, outcome.ID); !rec.LogsTruncated {
				t.Error("an output placed where it cannot be stored was not marked truncated")
			}
		})
	}
}

// A store that cannot take the output marks it truncated and fails nothing.
func TestCapture_StoreDown(t *testing.T) {
	t.Parallel()

	d, store := flaky(newRegistry())
	store.failLogs = true

	rec := &execution.Record{}
	d.collectLogs(t.Context(), nil, rec, &execution.Result{Logs: []byte("output")}, execution.Lease{})

	if !rec.LogsTruncated || rec.LogsAt.IsZero() {
		t.Errorf("record truncated %t at %s, want truncated and captured", rec.LogsTruncated, rec.LogsAt)
	}
}

// A cancelled run has no result to read, but its output is still captured in
// full before the provider is released.
func TestCapture_CancelledRunBeforeTheRelease(t *testing.T) {
	t.Parallel()

	p, reg := capturing(writes("cancelled output\n"))
	p.pollsToFinish = 100

	releaser := &capturingReleaser{capturingProvider: p}
	reg.providers["a"] = releaser

	ctx, cancel := context.WithCancel(t.Context())
	d := over(reg, cancelling(&p.scriptedProvider, cancel))

	o := begin(t, d)
	outcome, err := d.RunTask(ctx, o, containerTask(t, nil), nil, nil)
	d.Finish(t.Context(), o, nil, err)

	id := outcome.Attempts[0].ID

	if got := stored(t, reg, id); got != "cancelled output\n" || !releaser.capturedFirst {
		t.Errorf("stored %q, captured before the release %t; want the output whole first", got, releaser.capturedFirst)
	}

	if rec := record(t, reg, id); rec.State != execution.StateCancelled || rec.LogsAt.IsZero() || rec.Released.IsZero() {
		t.Errorf("record = %+v, want cancelled, captured and released", rec)
	}
}

// -------------------------------------------------------------------------
// SINK
// -------------------------------------------------------------------------

// chunkStore is chunks held in a map, as a sink's store and read reach them.
type chunkStore struct {
	chunks map[int64][]byte
	fail   error // when set, every write fails with it
}

// sink is a sink over the store, keeping limit bytes.
func (c *chunkStore) sink(limit int64) *logSink {
	return &logSink{limit: limit, store: c.store, read: c.read}
}

// store keeps chunk seq, dropping those before keepFrom and from seq on, as
// AppendLog does.
func (c *chunkStore) store(seq int64, data []byte, keepFrom int64) error {
	if c.fail != nil {
		return c.fail
	}

	maps.DeleteFunc(c.chunks, func(k int64, _ []byte) bool { return k < keepFrom || k >= seq })
	c.chunks[seq] = bytes.Clone(data)

	return nil
}

// read hands each chunk from fromSeq on to each, in order, as ReadLog does.
func (c *chunkStore) read(fromSeq int64, each func(int64, []byte) error) error {
	for _, seq := range slices.Sorted(maps.Keys(c.chunks)) {
		if seq < fromSeq {
			continue
		}

		if err := each(seq, c.chunks[seq]); err != nil {
			return err
		}
	}

	return nil
}

// full is a whole chunk of b.
func full(b byte) []byte {
	return []byte(strings.Repeat(string(b), execution.LogChunk))
}

// A resumed sink carries on from what is stored: after the last full chunk,
// or into the one still filling, from where the front was dropped.
func TestLogSink_Resumes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		chunks        map[int64][]byte
		len, start    int64
		nextSeq       int64
		nextChunkData string
	}{
		{name: "nothing stored", chunks: map[int64][]byte{}, nextChunkData: "z"},
		{
			name: "into a chunk filling", chunks: map[int64][]byte{0: full('a'), 1: []byte("ab")},
			len: execution.LogChunk + 2, nextSeq: 1, nextChunkData: "abz",
		},
		{
			name: "after a full chunk", chunks: map[int64][]byte{0: full('a')},
			len: execution.LogChunk, nextSeq: 1, nextChunkData: "z",
		},
		{
			name: "past a dropped front", chunks: map[int64][]byte{2: full('c'), 3: []byte("d")},
			len: 3*execution.LogChunk + 1, start: 2 * execution.LogChunk, nextSeq: 3, nextChunkData: "dz",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := &chunkStore{chunks: tc.chunks}
			s := store.sink(execution.DefaultMaxLogBytes)

			if err := s.resume(); err != nil || s.Len() != tc.len || s.start() != tc.start {
				t.Fatalf("resume() = %v at %d from %d, want %d from %d", err, s.Len(), s.start(), tc.len, tc.start)
			}

			if _, err := s.Write([]byte("z")); err != nil || s.flush() != nil {
				t.Fatalf("writing after the resume failed: %v", err)
			}

			if got := string(store.chunks[tc.nextSeq]); got != tc.nextChunkData {
				t.Errorf("chunk %d = %q, want %q", tc.nextSeq, got, tc.nextChunkData)
			}
		})
	}
}

// WriteTo stores what is buffered, then writes everything stored from the
// first chunk kept.
func TestLogSink_WriteTo(t *testing.T) {
	t.Parallel()

	store := &chunkStore{chunks: map[int64][]byte{}}
	s := store.sink(execution.DefaultMaxLogBytes)

	_, _ = s.Write([]byte("one\ntwo\n"))

	var out strings.Builder
	if n, err := s.WriteTo(&out); err != nil || out.String() != "one\ntwo\n" || n != 8 {
		t.Errorf("WriteTo() = %d, %v, wrote %q; want what was written", n, err, out.String())
	}
}

// A write the lease refused fails every write after it, while any other
// failure keeps the output for the next store write to take.
func TestLogSink_FailedWrites(t *testing.T) {
	t.Parallel()

	stale := &chunkStore{chunks: map[int64][]byte{}, fail: fmt.Errorf("held: %w", execution.ErrStale)}
	s := stale.sink(execution.DefaultMaxLogBytes)

	_, _ = s.Write([]byte("x"))

	if err := s.flush(); !errors.Is(s.failed(), execution.ErrStale) || err == nil {
		t.Fatalf("flush() = %v, failed() = %v; want the lease's refusal kept", err, s.failed())
	}

	if _, err := s.Write([]byte("y")); !errors.Is(err, execution.ErrStale) {
		t.Errorf("Write() after the refusal = %v, want the refusal", err)
	}

	down := &chunkStore{chunks: map[int64][]byte{}, fail: errors.New("store unreachable")}
	s = down.sink(execution.DefaultMaxLogBytes)

	_, _ = s.Write([]byte("kept"))

	if err := s.flush(); err == nil || s.failed() != nil {
		t.Fatalf("flush() = %v, failed() = %v; want the failure returned and not kept", err, s.failed())
	}

	down.fail = nil

	if err := s.flush(); err != nil || string(down.chunks[0]) != "kept" {
		t.Errorf("flush() once the store is back = %v, stored %q; want the output kept", err, down.chunks[0])
	}
}

// A chunk that fills while the store is down is stored by the next write,
// which then carries on.
func TestLogSink_FullChunkWaitsForTheStore(t *testing.T) {
	t.Parallel()

	store := &chunkStore{chunks: map[int64][]byte{}, fail: errors.New("store unreachable")}
	s := store.sink(execution.DefaultMaxLogBytes)

	if _, err := s.Write(full('a')); err == nil {
		t.Fatal("Write() filling a chunk the store refused succeeded")
	}

	store.fail = nil

	if _, err := s.Write([]byte("b")); err != nil || s.flush() != nil {
		t.Fatalf("Write() once the store is back = %v", err)
	}

	if len(store.chunks[0]) != execution.LogChunk || string(store.chunks[1]) != "b" {
		t.Errorf("stored chunks of %d and %q, want the full chunk, then b", len(store.chunks[0]), store.chunks[1])
	}
}

// -------------------------------------------------------------------------
// MORE FIXTURES
// -------------------------------------------------------------------------

// refreshingCapturer takes a fresh credential, so a capture refused as
// unauthorized is made again.
type refreshingCapturer struct {
	*capturingProvider
}

// Recredential accepts the new credential.
func (*refreshingCapturer) Recredential(context.Context, []byte) error { return nil }

// capturingReleaser holds its output and leaves a resource behind, recording
// whether the capture had finished when it was released.
type capturingReleaser struct {
	*capturingProvider

	finished      bool
	capturedFirst bool
}

// CaptureLogs captures, noting when a call has finished the output.
func (p *capturingReleaser) CaptureLogs(
	ctx context.Context, id execution.ID, log execution.Log, ended <-chan struct{},
) error {
	err := p.capturingProvider.CaptureLogs(ctx, id, log, ended)

	p.mu.Lock()
	p.finished = p.finished || err == nil
	p.mu.Unlock()

	return err
}

// Release records whether the capture finished before it.
func (p *capturingReleaser) Release(context.Context, execution.ID) error {
	p.mu.Lock()
	p.capturedFirst = p.finished
	p.mu.Unlock()

	return nil
}
