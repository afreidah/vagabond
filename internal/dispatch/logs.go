// -------------------------------------------------------------------------------
// Capturing Output
//
// Author: Alex Freidah
//
// An execution's output is copied into the store once it has ended, under the
// dispatch's lease, before its result is recorded and before anything is
// released: recording the result tells a waiting client to read the output,
// and releasing may destroy it. The copy is chunked as it streams, and output
// that cannot be read leaves the record marked truncated, never failed.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// captureTimeout bounds one capture, on its own context so a cancelled run's
// output is still read.
const captureTimeout = 5 * time.Minute

// -------------------------------------------------------------------------
// CAPTURE
// -------------------------------------------------------------------------

// capture copies what rec's execution printed into the store under key, and
// records on rec where its output stands. A provider that holds the output is
// read; any other's arrived with result, when there is one.
func (d *Dispatcher) capture(
	ctx context.Context, provider plugin.Provider, rec *execution.Record, result *execution.Result, key execution.Lease,
) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), captureTimeout)
	defer cancel()

	sink := &logSink{
		limit: d.maxLogBytes,
		store: func(seq int64, data []byte, keepFrom int64) error {
			return d.executions.AppendLog(ctx, rec.ID, seq, data, keepFrom, key)
		},
	}

	var err error

	switch reader, reads := provider.(plugin.LogReader); {
	case reads:
		// A retry on a fresh credential starts the output over, not after the first try
		err = authorizedErr(ctx, d, provider, func() error {
			if err := sink.restart(); err != nil {
				return err
			}

			// What arrived before a failed read is kept
			readErr := reader.ReadLogs(ctx, rec.ID, sink)

			return errors.Join(readErr, sink.flush())
		})

	case result != nil:
		if _, err = sink.Write(result.Logs); err == nil {
			err = sink.flush()
		}
	}

	rec.LogsAt = d.now()
	rec.LogsStart = sink.keep * execution.LogChunk
	rec.LogsTruncated = err != nil || (result != nil && result.LogsTruncated)
}

// -------------------------------------------------------------------------
// SINK
// -------------------------------------------------------------------------

// logSink cuts what is written to it into chunks and stores each as it fills
// through store, dropping the front once more than limit has been written.
// keep is the first chunk still stored.
type logSink struct {
	store func(seq int64, data []byte, keepFrom int64) error
	limit int64

	buf  []byte
	seq  int64
	end  int64
	keep int64
}

// Write buffers p, storing every chunk it fills.
func (s *logSink) Write(p []byte) (int, error) {
	written := 0

	for len(p) > 0 {
		if s.buf == nil {
			s.buf = make([]byte, 0, execution.LogChunk)
		}

		take := min(execution.LogChunk-len(s.buf), len(p))
		s.buf = append(s.buf, p[:take]...)
		s.end += int64(take)
		p = p[take:]
		written += take

		if len(s.buf) == execution.LogChunk {
			if err := s.flush(); err != nil {
				return written, err
			}
		}
	}

	return written, nil
}

// flush stores what is buffered as the next chunk, if anything is.
func (s *logSink) flush() error {
	if len(s.buf) == 0 {
		return nil
	}

	s.keep = execution.KeepFrom(s.end, s.limit)

	if err := s.store(s.seq, s.buf, s.keep); err != nil {
		return err
	}

	s.seq++
	s.buf = s.buf[:0]

	return nil
}

// restart begins the output again. Chunks a failed attempt stored are replaced
// with an empty chunk 0, which the first chunk of this attempt replaces in
// turn, so an attempt that prints nothing leaves nothing from the last.
func (s *logSink) restart() error {
	if s.seq > 0 {
		if err := s.store(0, []byte{}, 0); err != nil {
			return err
		}
	}

	s.buf, s.seq, s.end, s.keep = s.buf[:0], 0, 0, 0

	return nil
}
