// -------------------------------------------------------------------------------
// Capturing Output Tests
//
// Author: Alex Freidah
//
// Where an execution's output ends up: read from a provider that holds it or
// taken from the answer that carried it, cut into chunks with the front dropped
// past the ceiling, started over when a credential is refreshed, and marked
// truncated when it cannot all be read, never failing the run.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// readingProvider holds its executions' output, as Cloud Run does, and writes
// it with read.
type readingProvider struct {
	scriptedProvider

	read  func(attempt int, w io.Writer) error
	reads int
}

// ReadLogs counts the call and writes what read does.
func (p *readingProvider) ReadLogs(_ context.Context, _ execution.ID, w io.Writer) error {
	p.reads++

	return p.read(p.reads, w)
}

// reading registers a reading provider named "a" whose runs succeed at once.
func reading(read func(attempt int, w io.Writer) error) (*readingProvider, *fakeRegistry) {
	p := &readingProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded, read: read}

	reg := newRegistry(&p.scriptedProvider)
	reg.providers["a"] = p

	return p, reg
}

// writes is a read that writes text and succeeds.
func writes(text string) func(int, io.Writer) error {
	return func(_ int, w io.Writer) error {
		_, err := io.WriteString(w, text)

		return err
	}
}

// stored returns what the registry's store holds of id's output.
func stored(t *testing.T, reg *fakeRegistry, id execution.ID) string {
	t.Helper()

	var out strings.Builder

	if err := reg.executions.ReadLog(t.Context(), id, 0, &out); err != nil {
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

// A provider that holds the output is read once the run ends, and the record
// says when.
func TestCapture_ReadsTheProvider(t *testing.T) {
	t.Parallel()

	p, reg := reading(writes("line one\nline two\n"))
	outcome := runOnce(t, newDispatcher(t, reg))

	if got := stored(t, reg, outcome.ID); got != "line one\nline two\n" || p.reads != 1 {
		t.Errorf("stored %q after %d reads, want the output after one", got, p.reads)
	}

	rec := record(t, reg, outcome.ID)
	if rec.LogsAt.IsZero() || rec.LogsStart != 0 || rec.LogsTruncated {
		t.Errorf("record says output at %s from %d, truncated %t; want captured whole",
			rec.LogsAt, rec.LogsStart, rec.LogsTruncated)
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
	_, reg := reading(writes(head + "the end\n"))
	d := over(reg, WithSleeper(noWait), WithMaxLogBytes(execution.LogChunk))

	outcome := runOnce(t, d)

	if got := stored(t, reg, outcome.ID); got != "the end\n" {
		t.Errorf("stored %d bytes ending %q, want only the last chunk", len(got), got[max(0, len(got)-10):])
	}

	if rec := record(t, reg, outcome.ID); rec.LogsStart != 2*execution.LogChunk {
		t.Errorf("LogsStart = %d, want %d", rec.LogsStart, 2*execution.LogChunk)
	}
}

// A read refused as unauthorized is made again on a fresh credential, and the
// output starts over rather than following what the first try wrote.
func TestCapture_StartsOverOnAFreshCredential(t *testing.T) {
	t.Parallel()

	first := strings.Repeat("x", execution.LogChunk) + "cut short"
	p, reg := reading(func(attempt int, w io.Writer) error {
		if attempt == 1 {
			_, _ = io.WriteString(w, first)

			return plugin.ClassifyHTTP(http.StatusUnauthorized, 0, errors.New("expired token"))
		}

		_, err := io.WriteString(w, "second try\n")

		return err
	})

	refreshing := &refreshingReader{readingProvider: p}
	reg.providers["a"] = refreshing

	outcome := runOnce(t, newDispatcher(t, reg))

	if got := stored(t, reg, outcome.ID); got != "second try\n" || p.reads != 2 {
		t.Errorf("stored %q after %d reads, want only the second try after two", got, p.reads)
	}
}

// Output that cannot all be read keeps what arrived, is marked truncated, and
// leaves the run's verdict alone.
func TestCapture_MarksAFailedReadTruncated(t *testing.T) {
	t.Parallel()

	_, reg := reading(func(_ int, w io.Writer) error {
		_, _ = io.WriteString(w, "partial\n")

		return plugin.Infrastructure(errors.New("connection reset"))
	})

	outcome := runOnce(t, newDispatcher(t, reg))

	if !outcome.Succeeded() {
		t.Error("a failed read of the output failed the run")
	}

	if got := stored(t, reg, outcome.ID); got != "partial\n" {
		t.Errorf("stored %q, want what arrived", got)
	}

	if rec := record(t, reg, outcome.ID); !rec.LogsTruncated || rec.LogsAt.IsZero() {
		t.Errorf("record truncated %t at %s, want truncated and captured", rec.LogsTruncated, rec.LogsAt)
	}
}

// A store that cannot take the output marks it truncated and fails nothing.
func TestCapture_StoreDown(t *testing.T) {
	t.Parallel()

	d, store := flaky(newRegistry())
	store.failLogs = true

	rec := &execution.Record{}
	d.capture(t.Context(), &scriptedProvider{name: "a"}, rec, &execution.Result{Logs: []byte("output")}, execution.Lease{})

	if !rec.LogsTruncated || rec.LogsAt.IsZero() {
		t.Errorf("record truncated %t at %s, want truncated and captured", rec.LogsTruncated, rec.LogsAt)
	}
}

// A cancelled run has no result to read, but its output is still captured,
// before the provider is released.
func TestCapture_CancelledRunBeforeTheRelease(t *testing.T) {
	t.Parallel()

	p, reg := reading(writes("cancelled output\n"))
	p.pollsToFinish = 100

	releaser := &readingReleaser{readingProvider: p}
	reg.providers["a"] = releaser

	ctx, cancel := context.WithCancel(t.Context())
	d := over(reg, cancelling(&p.scriptedProvider, cancel))

	o := begin(t, d)
	outcome, err := d.RunTask(ctx, o, containerTask(t, nil), nil, nil)
	d.Finish(t.Context(), o, nil, err)

	id := outcome.Attempts[0].ID

	if got := stored(t, reg, id); got != "cancelled output\n" || releaser.releasedAfterReads != 1 {
		t.Errorf("stored %q, released after %d reads; want the output read before the release",
			got, releaser.releasedAfterReads)
	}

	if rec := record(t, reg, id); rec.State != execution.StateCancelled || rec.LogsAt.IsZero() || rec.Released.IsZero() {
		t.Errorf("record = %+v, want cancelled, captured and released", rec)
	}
}

// -------------------------------------------------------------------------
// MORE FIXTURES
// -------------------------------------------------------------------------

// refreshingReader takes a fresh credential, so a read refused as
// unauthorized is made again.
type refreshingReader struct {
	*readingProvider
}

// Recredential accepts the new credential.
func (*refreshingReader) Recredential(context.Context, []byte) error { return nil }

// readingReleaser holds its output and leaves a resource behind, recording
// how many reads had happened when it was released.
type readingReleaser struct {
	*readingProvider

	releasedAfterReads int
}

// Release records the reads made before it.
func (p *readingReleaser) Release(context.Context, execution.ID) error {
	p.releasedAfterReads = p.reads

	return nil
}
