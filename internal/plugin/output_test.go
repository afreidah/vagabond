// -------------------------------------------------------------------------------
// Log Output Tests
//
// Author: Alex Freidah
//
// The copies are told apart by what a failed read means, so each is driven
// through a read that fails partway and one that ends cleanly. The skip is
// driven across write boundaries, where an off-by-one would show.
// -------------------------------------------------------------------------------

package plugin

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// errSink stands in for a caller whose connection went away.
var errSink = errors.New("broken pipe")

// failingWriter refuses every write.
type failingWriter struct{}

// Write fails.
func (failingWriter) Write([]byte) (int, error) { return 0, errSink }

// cutReader yields text and then fails, as a dropped connection does.
func cutReader(text string) io.Reader {
	return io.MultiReader(strings.NewReader(text), iotest.ErrReader(errUnderlying))
}

// classOf returns the class err carries, empty when it carries none.
func classOf(err error) Class {
	if classified, ok := errors.AsType[*Error](err); ok {
		return classified.Class
	}

	return ""
}

// -------------------------------------------------------------------------
// WRITERS
// -------------------------------------------------------------------------

// A failed write is the caller's sink, so it is Internal and keeps its cause.
func TestLogOutput_ClassifiesAFailedWrite(t *testing.T) {
	_, err := LogOutput(failingWriter{}).Write([]byte("line\n"))

	if classOf(err) != ClassInternal || !errors.Is(err, errSink) {
		t.Errorf("Write() = %v, want an Internal error wrapping %v", err, errSink)
	}
}

// The skip spans writes: the bytes passed on are exactly those past n, however
// the writes fall around it.
func TestSkipWriter_SkipsAcrossWrites(t *testing.T) {
	tests := []struct {
		name   string
		skip   int64
		writes []string
		want   string
	}{
		{name: "nothing skipped", skip: 0, writes: []string{"abc", "def"}, want: "abcdef"},
		{name: "inside the first write", skip: 2, writes: []string{"abc", "def"}, want: "cdef"},
		{name: "at a boundary", skip: 3, writes: []string{"abc", "def"}, want: "def"},
		{name: "inside a later write", skip: 4, writes: []string{"abc", "def"}, want: "ef"},
		{name: "past everything", skip: 10, writes: []string{"abc", "def"}, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer

			w := SkipWriter(&out, tc.skip)

			for _, write := range tc.writes {
				n, err := w.Write([]byte(write))
				if err != nil || n != len(write) {
					t.Fatalf("Write(%q) = %d, %v, want %d, nil", write, n, err, len(write))
				}
			}

			if out.String() != tc.want {
				t.Errorf("passed on %q, want %q", out.String(), tc.want)
			}
		})
	}
}

// A failed write past the skip counts the skipped bytes as written, so the
// count stays a position in what was given.
func TestSkipWriter_CountsSkippedBytesOnAFailedWrite(t *testing.T) {
	n, err := SkipWriter(failingWriter{}, 2).Write([]byte("abcd"))

	if n != 2 || !errors.Is(err, errSink) {
		t.Errorf("Write() = %d, %v, want 2, %v", n, err, errSink)
	}
}

// -------------------------------------------------------------------------
// COPYING
// -------------------------------------------------------------------------

// Both copies pass everything through, read a byte at a time, and end cleanly
// at the end of the output.
func TestCopy_CopiesToTheEnd(t *testing.T) {
	copies := map[string]func(io.Writer, io.Reader) error{"live": CopyLive, "all": CopyAll}

	for name, copyFn := range copies {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer

			err := copyFn(&out, iotest.OneByteReader(strings.NewReader("one\ntwo\n")))
			if err != nil || out.String() != "one\ntwo\n" {
				t.Errorf("copy = %q, %v, want everything and nil", out.String(), err)
			}
		})
	}
}

// A live view ends quietly on a failed read, keeping what arrived.
func TestCopyLive_EndsQuietlyOnAFailedRead(t *testing.T) {
	var out bytes.Buffer

	if err := CopyLive(&out, cutReader("partial\n")); err != nil || out.String() != "partial\n" {
		t.Errorf("CopyLive() = %q, %v, want the partial output and nil", out.String(), err)
	}
}

// A read of the whole output reports a failed read as the platform's, after
// writing what arrived.
func TestCopyAll_ReportsAFailedRead(t *testing.T) {
	var out bytes.Buffer

	err := CopyAll(&out, cutReader("partial\n"))

	if classOf(err) != ClassInfrastructure || !errors.Is(err, errUnderlying) {
		t.Errorf("CopyAll() = %v, want an Infrastructure error wrapping %v", err, errUnderlying)
	}

	if out.String() != "partial\n" {
		t.Errorf("wrote %q, want the partial output", out.String())
	}
}

// Both copies return a failed write as Internal, whatever the read would do.
func TestCopy_ReturnsAFailedWrite(t *testing.T) {
	copies := map[string]func(io.Writer, io.Reader) error{"live": CopyLive, "all": CopyAll}

	for name, copyFn := range copies {
		t.Run(name, func(t *testing.T) {
			err := copyFn(failingWriter{}, strings.NewReader("line\n"))

			if classOf(err) != ClassInternal || !errors.Is(err, errSink) {
				t.Errorf("copy = %v, want an Internal error wrapping %v", err, errSink)
			}
		})
	}
}
