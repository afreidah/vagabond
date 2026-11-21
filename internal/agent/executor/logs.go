// -------------------------------------------------------------------------------
// Following Output
//
// Author: Alex Freidah
//
// Copying a workload's output from a byte offset as it is written, the same
// for every executor: each says only how to read what follows an offset. A
// node keeps only the end of long output, so a read may begin past where the
// last ended, and the writer is told where before anything past the gap.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// logPoll is how often a log that is still being written is read again.
const logPoll = 250 * time.Millisecond

// output is what one read of a workload's output gave: data beginning at
// offset, and whether the workload has ended with all of it read.
type output struct {
	data   []byte
	offset int64
	ended  bool
}

// follow copies a workload's output from offset on to w, read by read through
// next, until it has ended with all of it read or ctx ends, reading again every
// logPoll while nothing new has come. A read beginning past where the last
// ended is placed through w.StartsAt first.
func follow(ctx context.Context, offset int64, w execution.LogWriter, next func(offset int64) (output, error)) error {
	for {
		out, err := next(offset)
		if err != nil {
			return err
		}

		if offset, err = put(w, out, offset); err != nil {
			return err
		}

		switch {
		case out.ended:
			return nil
		case len(out.data) == 0:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(logPoll):
			}
		}
	}
}

// put writes one read's data to w, placed through w.StartsAt first when it
// begins past offset, and returns where the next read begins.
func put(w execution.LogWriter, out output, offset int64) (int64, error) {
	// Output dropped from the front moves where this read begins
	if out.offset > offset {
		if err := w.StartsAt(out.offset); err != nil {
			return offset, err
		}

		offset = out.offset
	}

	// Written only when there is something, since a write sends a chunk
	if len(out.data) == 0 {
		return offset, nil
	}

	n, err := w.Write(out.data)

	return offset + int64(n), err
}
