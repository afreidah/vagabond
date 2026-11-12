// -------------------------------------------------------------------------------
// Log Output
//
// Author: Alex Freidah
//
// Copying a task's output from a platform to a caller. A failed write is the
// caller's sink breaking, an Internal error; a failed read is the platform's.
// A live view ends quietly on a failed read, since a broken stream fails no
// work, while a read of the whole output reports it, since the copy is then
// incomplete.
// -------------------------------------------------------------------------------

package plugin

import (
	"errors"
	"fmt"
	"io"
)

// copyBuffer is how much one read of a platform's output takes.
const copyBuffer = 32 << 10

// LogOutput wraps w so a failed write is an Internal error.
func LogOutput(w io.Writer) io.Writer {
	return logOutput{w: w}
}

// logOutput is the writer LogOutput returns.
type logOutput struct {
	w io.Writer
}

// Write writes p, classifying a failure.
func (o logOutput) Write(p []byte) (int, error) {
	n, err := o.w.Write(p)
	if err != nil {
		return n, Internal(fmt.Errorf("writing log output: %w", err))
	}

	return n, nil
}

// SkipWriter returns a writer that discards the first n bytes written to it,
// the ones a resuming caller already has, and passes the rest to w.
func SkipWriter(w io.Writer, n int64) io.Writer {
	return &skipWriter{w: w, skip: n}
}

// skipWriter is the writer SkipWriter returns.
type skipWriter struct {
	w    io.Writer
	skip int64
}

// Write passes on what of p lies past the skipped bytes, counting the skipped
// ones as written.
func (s *skipWriter) Write(p []byte) (int, error) {
	if s.skip >= int64(len(p)) {
		s.skip -= int64(len(p))

		return len(p), nil
	}

	skipped := int(s.skip)
	s.skip = 0

	n, err := s.w.Write(p[skipped:])

	return skipped + n, err
}

// CopyLive copies r to w until r ends, for a live view. A failed read ends the
// copy quietly; a failed write is returned.
func CopyLive(w io.Writer, r io.Reader) error {
	_, err := copyLog(w, r)

	return err
}

// CopyAll copies r to w until r ends, for a read of the whole output. A failed
// read is an Infrastructure error, since what was written is incomplete.
func CopyAll(w io.Writer, r io.Reader) error {
	readErr, err := copyLog(w, r)
	if err != nil {
		return err
	}

	if readErr != nil {
		return Infrastructure(fmt.Errorf("reading log output: %w", readErr))
	}

	return nil
}

// copyLog copies r to w, returning a failed read and a failed write apart. The
// end of r is neither.
func copyLog(w io.Writer, r io.Reader) (readErr, writeErr error) {
	out := LogOutput(w)
	buf := make([]byte, copyBuffer)

	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return nil, werr
			}
		}

		switch {
		case errors.Is(err, io.EOF):
			return nil, nil
		case err != nil:
			return err, nil
		}
	}
}
