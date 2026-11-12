// -------------------------------------------------------------------------------
// Stored Output Contract
//
// Author: Alex Freidah
//
// What an execution store promises about output: chunks read back in order, a
// write at chunk 0 starts the output over so a retried capture cannot repeat
// it, the front drops as the ceiling asks, and pruning takes only what ended
// before the cutoff.
// -------------------------------------------------------------------------------

package statetest

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// INTERFACE
// -------------------------------------------------------------------------

// LogStore is the part of an execution store the output rules cover.
type LogStore interface {
	Create(ctx context.Context, r *execution.Record) error
	AppendLog(ctx context.Context, id execution.ID, seq int64, data []byte, keepFrom int64) error
	ReadLog(ctx context.Context, id execution.ID, fromSeq int64, w io.Writer) error
	PruneLogs(ctx context.Context, before time.Time) (int, error)
}

// -------------------------------------------------------------------------
// CONTRACT
// -------------------------------------------------------------------------

// LogContract runs every output rule against a fresh store from newStore.
func LogContract(t *testing.T, newStore func(t *testing.T) LogStore) {
	for _, r := range logRules {
		t.Run(r.name, func(t *testing.T) { r.check(t, newStore(t)) })
	}
}

// logRule is one promise about output, checked against a fresh store.
type logRule struct {
	name  string
	check func(t *testing.T, s LogStore)
}

// logRules are the promises LogContract checks.
var logRules = []logRule{
	{"chunks read back in order", readsInOrder},
	{"a write at chunk 0 starts over", startsOver},
	{"the front drops past the ceiling", dropsTheFront},
	{"a read begins at its chunk", readsFromAChunk},
	{"executions keep their own output", keepsExecutionsApart},
	{"an execution with no output reads empty", readsEmpty},
	{"pruning takes only what ended before the cutoff", prunesBeforeTheCutoff},
}

// -------------------------------------------------------------------------
// RULES
// -------------------------------------------------------------------------

// readsInOrder: chunks read back in the order written, bytes that are not
// UTF-8 included, since output need not be text.
func readsInOrder(t *testing.T, s LogStore) {
	id := created(t, s, time.Time{})
	appendAll(t, s, id, 0, "one,", "ok\x00\xff\xfe,", "three")

	if got := read(t, s, id, 0); got != "one,ok\x00\xff\xfe,three" {
		t.Errorf("read %q, want every chunk in order", got)
	}
}

// startsOver: a write at chunk 0 replaces everything stored before it.
func startsOver(t *testing.T, s LogStore) {
	id := created(t, s, time.Time{})
	appendAll(t, s, id, 0, "first try,", "cut short")
	appendAll(t, s, id, 0, "second try")

	if got := read(t, s, id, 0); got != "second try" {
		t.Errorf("read %q, want only the second capture", got)
	}
}

// dropsTheFront: a write's keepFrom deletes the chunks before it.
func dropsTheFront(t *testing.T, s LogStore) {
	id := created(t, s, time.Time{})
	appendAll(t, s, id, 0, "a,", "b,")
	appendChunk(t, s, id, 2, "c", 2)

	if got := read(t, s, id, 0); got != "c" {
		t.Errorf("read %q, want only the chunks kept", got)
	}
}

// readsFromAChunk: a read from a chunk skips the ones before it.
func readsFromAChunk(t *testing.T, s LogStore) {
	id := created(t, s, time.Time{})
	appendAll(t, s, id, 0, "a,", "b,", "c")

	if got := read(t, s, id, 1); got != "b,c" {
		t.Errorf("read %q from chunk 1, want b,c", got)
	}
}

// keepsExecutionsApart: one execution's chunks never read as another's.
func keepsExecutionsApart(t *testing.T, s LogStore) {
	first, second := created(t, s, time.Time{}), created(t, s, time.Time{})
	appendAll(t, s, first, 0, "first")
	appendAll(t, s, second, 0, "second")

	if got := read(t, s, first, 0); got != "first" {
		t.Errorf("read %q, want only the first execution's output", got)
	}
}

// readsEmpty: an execution with no output stored reads as nothing.
func readsEmpty(t *testing.T, s LogStore) {
	if got := read(t, s, created(t, s, time.Time{}), 0); got != "" {
		t.Errorf("read %q, want nothing", got)
	}
}

// prunesBeforeTheCutoff: pruning takes the output of executions that ended
// before the cutoff, leaves recent and running ones, and finds nothing the
// second time.
func prunesBeforeTheCutoff(t *testing.T, s LogStore) {
	now := time.Now().UTC().Truncate(time.Second)

	old := created(t, s, now.Add(-48*time.Hour))
	recent := created(t, s, now.Add(-time.Minute))
	running := created(t, s, time.Time{})

	for _, id := range []execution.ID{old, recent, running} {
		appendAll(t, s, id, 0, "output")
	}

	pruned, err := s.PruneLogs(t.Context(), now.Add(-24*time.Hour))
	if err != nil || pruned != 1 {
		t.Fatalf("PruneLogs() = %d, %v; want 1", pruned, err)
	}

	if got := read(t, s, old, 0); got != "" {
		t.Errorf("read %q of the pruned execution, want nothing", got)
	}

	for _, id := range []execution.ID{recent, running} {
		if got := read(t, s, id, 0); got != "output" {
			t.Errorf("read %q of %s, want its output kept", got, id)
		}
	}

	if again, err := s.PruneLogs(t.Context(), now.Add(-24*time.Hour)); err != nil || again != 0 {
		t.Errorf("PruneLogs() again = %d, %v; want 0", again, err)
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// created records an execution, ended at ended when that is set and running
// otherwise, returning its ID.
func created(t *testing.T, s LogStore, ended time.Time) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	r := &execution.Record{
		ID:        id,
		State:     execution.StateRunning,
		UpdatedAt: time.Now().UTC(),
		Job:       "ci",
		Task:      "test",
	}

	if !ended.IsZero() {
		r.State, r.StartedAt, r.EndedAt = execution.StateSucceeded, ended.Add(-time.Second), ended
	}

	if err := s.Create(t.Context(), r); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	return id
}

// appendAll stores chunks from seq on, one after another, keeping everything.
func appendAll(t *testing.T, s LogStore, id execution.ID, seq int64, chunks ...string) {
	t.Helper()

	for i, chunk := range chunks {
		appendChunk(t, s, id, seq+int64(i), chunk, 0)
	}
}

// appendChunk stores one chunk.
func appendChunk(t *testing.T, s LogStore, id execution.ID, seq int64, chunk string, keepFrom int64) {
	t.Helper()

	if err := s.AppendLog(t.Context(), id, seq, []byte(chunk), keepFrom); err != nil {
		t.Fatalf("AppendLog(%d) = %v", seq, err)
	}
}

// read returns id's stored output from chunk fromSeq on.
func read(t *testing.T, s LogStore, id execution.ID, fromSeq int64) string {
	t.Helper()

	var out strings.Builder

	if err := s.ReadLog(t.Context(), id, fromSeq, &out); err != nil {
		t.Fatalf("ReadLog() = %v", err)
	}

	return out.String()
}
