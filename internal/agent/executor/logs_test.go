// -------------------------------------------------------------------------------
// Following Output Tests
//
// Author: Alex Freidah
//
// Reading a workload's output from an offset: a read past a gap is placed
// before it is written, and a log past the cap has its front freed while its
// offsets stay as they were, a read before the kept part beginning where it
// does.
// -------------------------------------------------------------------------------

package executor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// FOLLOWING
// -------------------------------------------------------------------------

// A read that begins past where the last ended is placed before it is written,
// and each read is asked for from where the last left off.
func TestFollow_PlacesAReadPastAGap(t *testing.T) {
	t.Parallel()

	reads := []output{
		{data: []byte("a"), offset: 0},
		{data: []byte("b"), offset: execution.LogChunk},
		{ended: true, offset: execution.LogChunk + 1},
	}

	var asked []int64

	var out bytes.Buffer

	w := &execution.PlacedWriter{Writer: &out}

	err := follow(t.Context(), 0, w, func(offset int64) (output, error) {
		asked = append(asked, offset)
		next := reads[0]
		reads = reads[1:]

		return next, nil
	})

	if err != nil || out.String() != "ab" || w.Start != execution.LogChunk {
		t.Errorf("follow() = %v, wrote %q from %d; want ab from %d", err, out.String(), w.Start, execution.LogChunk)
	}

	if want := []int64{0, 1, execution.LogChunk + 1}; !slices.Equal(asked, want) {
		t.Errorf("reads asked from %v, want %v", asked, want)
	}
}

// A read that fails ends the follow with its error, and one with nothing new
// waits on ctx, ending with it.
func TestFollow_EndsOnAFailedReadOrItsContext(t *testing.T) {
	t.Parallel()

	failed := errors.New("daemon gone")
	w := &execution.PlacedWriter{Writer: &bytes.Buffer{}}

	err := follow(t.Context(), 0, w, func(int64) (output, error) { return output{}, failed })
	if !errors.Is(err, failed) {
		t.Errorf("follow() = %v, want the read's error", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = follow(ctx, 0, w, func(int64) (output, error) { return output{}, nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("follow() = %v, want its context's end", err)
	}
}

// -------------------------------------------------------------------------
// TRIMMING
// -------------------------------------------------------------------------

// A log past the cap has its front freed, its length kept, and a read from its
// first byte begins where the kept part does.
func TestContainerd_TrimLogFreesTheFront(t *testing.T) {
	t.Parallel()

	c := &Containerd{cfg: Config{MaxLogBytes: execution.LogChunk}}
	path := filepath.Join(t.TempDir(), "exec.log")
	written := append(bytes.Repeat([]byte("x"), 3*execution.LogChunk), "the end\n"...)

	if err := os.WriteFile(path, written, 0o600); err != nil {
		t.Fatal(err)
	}

	before := allocated(t, path)

	if err := c.trimLog(path); errors.Is(err, syscall.EOPNOTSUPP) {
		t.Skipf("the test's filesystem cannot free part of a file: %v", err)
	} else if err != nil {
		t.Fatalf("trimLog() = %v", err)
	}

	if after := allocated(t, path); after >= before {
		t.Errorf("%d bytes allocated after trimming, %d before; want fewer", after, before)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = file.Close() }()

	if info, _ := file.Stat(); info.Size() != int64(len(written)) {
		t.Errorf("size after trimming = %d, want %d", info.Size(), len(written))
	}

	out, err := c.readLog(file, 0, make([]byte, logRead))
	if err != nil || out.offset != 3*execution.LogChunk || string(out.data) != "the end\n" {
		t.Errorf("readLog() = %q from %d, %v; want the end from %d", out.data, out.offset, err, 3*execution.LogChunk)
	}
}

// A log within the cap, and one released before it is reached, are left as
// they are.
func TestContainerd_TrimLogLeavesWhatIsWithinTheCap(t *testing.T) {
	t.Parallel()

	c := &Containerd{cfg: Config{MaxLogBytes: execution.LogChunk}}
	path := filepath.Join(t.TempDir(), "exec.log")

	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.trimLog(path); err != nil {
		t.Errorf("trimLog() of a short log = %v", err)
	}

	if got, _ := os.ReadFile(path); string(got) != "short\n" {
		t.Errorf("log after trimming = %q, want it whole", got)
	}

	if err := c.trimLog(filepath.Join(t.TempDir(), "released.log")); err != nil {
		t.Errorf("trimLog() of a released log = %v", err)
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// allocated is how many bytes of disk the file at path takes, counted in the
// 512-byte blocks stat reports, which a freed front no longer holds.
func allocated(t *testing.T, path string) int64 {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	return info.Sys().(*syscall.Stat_t).Blocks * 512
}
