// -------------------------------------------------------------------------------
// Stored Output Contract
//
// Author: Alex Freidah
//
// What an execution store promises about output: chunks read back in order and
// named, a write replaces its chunk and drops those after it so the last chunk
// grows in place, the front drops as the ceiling asks, and pruning takes only
// what ended before the cutoff.
// -------------------------------------------------------------------------------

package statetest

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// CONTRACT
// -------------------------------------------------------------------------

// LogContract runs every output rule against a fresh store from newStore.
func LogContract(t *testing.T, newStore func(t *testing.T) Store) {
	check(t, logRules, newStore)
}

// logRules are the promises LogContract checks.
var logRules = []rule[Store]{
	{"chunks read back in order", readsInOrder},
	{"a write at chunk 0 starts over", startsOver},
	{"the last chunk grows in place", growsInPlace},
	{"the front drops past the ceiling", dropsTheFront},
	{"a read names the chunks it hands over", namesChunks},
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
func readsInOrder(t *testing.T, s Store) {
	o := logged(t, s, time.Time{})
	appendAll(t, s, o, 0, "one,", "ok\x00\xff\xfe,", "three")

	if got := read(t, s, o, 0); got != "one,ok\x00\xff\xfe,three" {
		t.Errorf("read %q, want every chunk in order", got)
	}
}

// startsOver: a write at chunk 0 replaces everything stored before it.
func startsOver(t *testing.T, s Store) {
	o := logged(t, s, time.Time{})
	appendAll(t, s, o, 0, "first try,", "cut short")
	appendAll(t, s, o, 0, "second try")

	if got := read(t, s, o, 0); got != "second try" {
		t.Errorf("read %q, want only the second capture", got)
	}
}

// growsInPlace: writing the last chunk again replaces it, as a capture does
// each time more output arrives.
func growsInPlace(t *testing.T, s Store) {
	o := logged(t, s, time.Time{})
	appendAll(t, s, o, 0, "full,")
	appendChunk(t, s, o, 1, "gro", 0)
	appendChunk(t, s, o, 1, "grows", 0)

	if got := read(t, s, o, 0); got != "full,grows" {
		t.Errorf("read %q, want the last chunk as last written", got)
	}
}

// namesChunks: a read hands each chunk over with its own number, so a reader
// sees where the output it asked for was dropped.
func namesChunks(t *testing.T, s Store) {
	o := logged(t, s, time.Time{})
	appendAll(t, s, o, 0, "a,", "b,")
	appendChunk(t, s, o, 2, "c", 2)

	var seqs []int64

	err := s.ReadLog(t.Context(), o.id, 0, func(seq int64, _ []byte) error {
		seqs = append(seqs, seq)

		return nil
	})
	if err != nil || !slices.Equal(seqs, []int64{2}) {
		t.Errorf("ReadLog() handed over chunks %v, %v; want only chunk 2", seqs, err)
	}
}

// dropsTheFront: a write's keepFrom deletes the chunks before it.
func dropsTheFront(t *testing.T, s Store) {
	o := logged(t, s, time.Time{})
	appendAll(t, s, o, 0, "a,", "b,")
	appendChunk(t, s, o, 2, "c", 2)

	if got := read(t, s, o, 0); got != "c" {
		t.Errorf("read %q, want only the chunks kept", got)
	}
}

// readsFromAChunk: a read from a chunk skips the ones before it.
func readsFromAChunk(t *testing.T, s Store) {
	o := logged(t, s, time.Time{})
	appendAll(t, s, o, 0, "a,", "b,", "c")

	if got := read(t, s, o, 1); got != "b,c" {
		t.Errorf("read %q from chunk 1, want b,c", got)
	}
}

// keepsExecutionsApart: one execution's chunks never read as another's.
func keepsExecutionsApart(t *testing.T, s Store) {
	first, second := logged(t, s, time.Time{}), logged(t, s, time.Time{})
	appendAll(t, s, first, 0, "first")
	appendAll(t, s, second, 0, "second")

	if got := read(t, s, first, 0); got != "first" {
		t.Errorf("read %q, want only the first execution's output", got)
	}
}

// readsEmpty: an execution with no output stored reads as nothing.
func readsEmpty(t *testing.T, s Store) {
	if got := read(t, s, logged(t, s, time.Time{}), 0); got != "" {
		t.Errorf("read %q, want nothing", got)
	}
}

// prunesBeforeTheCutoff: pruning takes the output of executions that ended
// before the cutoff, leaves recent and running ones, and finds nothing the
// second time.
func prunesBeforeTheCutoff(t *testing.T, s Store) {
	now := time.Now().UTC().Truncate(time.Second)

	old := logged(t, s, now.Add(-48*time.Hour))
	recent := logged(t, s, now.Add(-time.Minute))
	running := logged(t, s, time.Time{})

	for _, o := range []output{old, recent, running} {
		appendAll(t, s, o, 0, "output")
	}

	pruned, err := s.PruneLogs(t.Context(), now.Add(-24*time.Hour))
	if err != nil || pruned != 1 {
		t.Fatalf("PruneLogs() = %d, %v; want 1", pruned, err)
	}

	if got := read(t, s, old, 0); got != "" {
		t.Errorf("read %q of the pruned execution, want nothing", got)
	}

	for _, o := range []output{recent, running} {
		if got := read(t, s, o, 0); got != "output" {
			t.Errorf("read %q of %s, want its output kept", got, o.id)
		}
	}

	if again, err := s.PruneLogs(t.Context(), now.Add(-24*time.Hour)); err != nil || again != 0 {
		t.Errorf("PruneLogs() again = %d, %v; want 0", again, err)
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// output is an execution whose output a rule writes, and the lease the
// writes are made under.
type output struct {
	id    execution.ID
	lease execution.Lease
}

// logged records an execution in a dispatch of its own, ended at ended when
// that is set and running otherwise.
func logged(t *testing.T, s Store, ended time.Time) output {
	t.Helper()

	d := dispatched(t, s, held())

	return output{id: recorded(t, s, d, ended).ID, lease: d.Lease()}
}

// appendAll stores chunks from seq on, one after another, keeping everything.
func appendAll(t *testing.T, s Store, o output, seq int64, chunks ...string) {
	t.Helper()

	for i, chunk := range chunks {
		appendChunk(t, s, o, seq+int64(i), chunk, 0)
	}
}

// appendChunk stores one chunk, failing the test if it is refused.
func appendChunk(t *testing.T, s Store, o output, seq int64, chunk string, keepFrom int64) {
	t.Helper()

	if err := s.AppendLog(t.Context(), o.id, seq, []byte(chunk), keepFrom, o.lease); err != nil {
		t.Fatalf("AppendLog(%d) = %v", seq, err)
	}
}

// read returns o's stored output from chunk fromSeq on, failing the test if a
// chunk is handed over out of order.
func read(t *testing.T, s Store, o output, fromSeq int64) string {
	t.Helper()

	var out strings.Builder

	last := fromSeq - 1

	err := s.ReadLog(t.Context(), o.id, fromSeq, func(seq int64, data []byte) error {
		if seq <= last {
			t.Errorf("chunk %d handed over after chunk %d", seq, last)
		}

		last = seq
		out.Write(data)

		return nil
	})
	if err != nil {
		t.Fatalf("ReadLog() = %v", err)
	}

	return out.String()
}
