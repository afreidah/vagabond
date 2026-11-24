// -------------------------------------------------------------------------------
// Capturing Output
//
// Author: Alex Freidah
//
// An execution's output is copied into the store while it runs, under the
// dispatch's lease, by one capture from submission until the provider has
// handed over all of it, and every client reads the store. A capture resumes
// from what is stored, so one taken over by another server carries on where
// the last stopped. The result is recorded only after the capture ends, since
// recording it tells a waiting client the output is whole, and nothing is
// released before then, since releasing may destroy it. A capture that cannot
// finish leaves the record marked truncated, never failed.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/backoff"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/panics"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

const (
	captureTimeout  = 5 * time.Minute  // how long a capture may take to finish once the workload has ended
	captureAttempts = 3                // failed calls after the workload ended before the output is kept as it is
	captureRetry    = time.Second      // the wait before the first call again
	captureRetryMax = 30 * time.Second // the longest wait between calls
	flushInterval   = time.Second      // how long arrived output waits to be stored
)

// -------------------------------------------------------------------------
// CAPTURE
// -------------------------------------------------------------------------

// capture copies one execution's output into the store while it runs. err is
// why it stopped short, read once done has closed.
type capture struct {
	sink   *logSink
	ended  chan struct{} // closed once the workload has ended
	end    func()        // closes ended; safe to repeat
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// startCapture begins copying id's output into the store under key, from what
// is stored already, when its provider holds the output. Otherwise it returns
// nil, and collect writes the output that came with the result.
func (d *Dispatcher) startCapture(
	ctx context.Context, provider plugin.Provider, id execution.ID, key execution.Lease,
) *capture {
	capturer, ok := provider.(plugin.LogCapturer)
	if !ok {
		return nil
	}

	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ended := make(chan struct{})

	c := &capture{
		sink:   d.newSink(ctx, id, key),
		ended:  ended,
		end:    sync.OnceFunc(func() { close(ended) }),
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go c.run(ctx, d, provider, capturer, id)

	return c
}

// run calls the provider's capture until it returns nil, the output whole,
// storing what arrives in the meantime every flushInterval and the rest when
// it ends. A call that returns anything else is logged and made again after a
// backoff, which a call that added output resets; once the workload has
// ended, captureAttempts of them end the capture. A write the lease refused
// ends it at once, as does a panic.
func (c *capture) run(
	ctx context.Context, d *Dispatcher, provider plugin.Provider, capturer plugin.LogCapturer, id execution.ID,
) {
	defer close(c.done)
	defer panics.Catch(ctx, d.logger, "capturing the output of "+id.String(), func(err error) { c.err = err })

	if c.err = c.resume(ctx, d, id); c.err != nil {
		return
	}

	flushing, stopFlushing := context.WithCancel(ctx)
	defer stopFlushing()

	go c.sink.flushEvery(flushing, d, id)

	// However the capture ends, what arrived is stored
	defer func() {
		if err := c.sink.flush(); c.err == nil {
			c.err = err
		}
	}()

	call := func() error {
		err := authorizedErr(ctx, d, provider, func() error { return capturer.CaptureLogs(ctx, id, c.sink, c.ended) })
		if err == nil {
			err = c.sink.flush()
		}

		return err
	}

	c.err = c.calls(ctx, d, id, call)
}

// resume reads back what another server stored, after a backoff each time the
// store does not answer, until it does or the capture is stopped.
func (c *capture) resume(ctx context.Context, d *Dispatcher, id execution.ID) error {
	for n := 0; ; n++ {
		err := c.sink.resume()
		if err == nil {
			return nil
		}

		d.logger.WarnContext(ctx, "reading stored output back", "execution", id, "error", err)

		if err := d.sleep(ctx, backoff.Exponential(captureRetry, captureRetryMax, n)); err != nil {
			return err
		}
	}
}

// calls makes call until the capture is over, returning why it stopped short,
// or nil with the output whole.
func (c *capture) calls(ctx context.Context, d *Dispatcher, id execution.ID, call func() error) error {
	for retries, failedAfterEnd := 0, 0; ; retries++ {
		finished, before := plugin.Ended(c.ended), c.sink.Len()

		err := call()
		if over, why := c.over(ctx, err); over {
			return why
		}

		d.logger.WarnContext(ctx, "capturing output", "execution", id, "error", err)

		if finished {
			if failedAfterEnd++; failedAfterEnd >= captureAttempts {
				return err
			}
		}

		if c.sink.Len() > before {
			retries = 0
		}

		if err := c.pause(ctx, d, backoff.Exponential(captureRetry, captureRetryMax, retries), finished); err != nil {
			return err
		}
	}
}

// over reports whether a call that returned err ends the capture, and why it
// stopped short: a write the lease refused, whatever the provider made of it,
// or ctx ending. A call returning nil has the output whole.
func (c *capture) over(ctx context.Context, err error) (bool, error) {
	switch failed := c.sink.failed(); {
	case failed != nil:
		return true, failed
	case err == nil:
		return true, nil
	case ctx.Err() != nil:
		return true, ctx.Err()
	default:
		return false, nil
	}
}

// pause waits out a backoff before the next call, cut short when the workload
// ends during it unless it had ended already.
func (c *capture) pause(ctx context.Context, d *Dispatcher, wait time.Duration, finished bool) error {
	if finished {
		return d.sleep(ctx, wait)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-c.ended:
			cancel()
		case <-ctx.Done():
		}
	}()

	err := d.sleep(ctx, wait)
	if plugin.Ended(c.ended) {
		return nil
	}

	return err
}

// stop ends the capture, whatever it holds, and waits for it. Safe on nil and
// to repeat.
func (c *capture) stop() {
	if c == nil {
		return
	}

	c.cancel()
	<-c.done
}

// collectLogs records on rec where its output stands once nothing more of it
// can come. A capture is told the workload has ended and waited for, within
// captureTimeout; with none, the output that came with result is stored.
func (d *Dispatcher) collectLogs(
	ctx context.Context, c *capture, rec *execution.Record, result *execution.Result, key execution.Lease,
) {
	var (
		sink *logSink
		err  error
	)

	switch {
	case c != nil:
		c.end()

		// Past the timeout, what the capture stored is kept as it is
		select {
		case <-c.done:
		case <-time.After(captureTimeout):
			c.stop()
		}

		sink, err = c.sink, c.err

	case result != nil:
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), captureTimeout)
		defer cancel()

		sink = d.newSink(ctx, rec.ID, key)

		if _, err = sink.Write(result.Logs); err == nil {
			err = sink.flush()
		}
	}

	if err != nil {
		d.logger.WarnContext(ctx, "output left incomplete", "execution", rec.ID, "error", err)
	}

	rec.LogsAt = d.now()
	rec.LogsTruncated = err != nil || (result != nil && result.LogsTruncated)

	if sink != nil {
		rec.LogsStart = sink.start()
	}
}

// -------------------------------------------------------------------------
// SINK
// -------------------------------------------------------------------------

// logSink is an execution's log as a capture writes it. What is written is cut
// into chunks, each stored through store as it fills and the one filling at
// least every flushInterval, the front dropped once more than limit has been
// written. keep is the first chunk still stored and seq the one filling. A
// store write that fails keeps what it would have stored for the next one,
// unless the lease moved, which fails every write after it.
type logSink struct {
	store func(seq int64, data []byte, keepFrom int64) error
	read  func(fromSeq int64, each func(seq int64, data []byte) error) error
	limit int64

	mu    sync.Mutex
	buf   []byte
	seq   int64
	end   int64
	keep  int64
	dirty bool // buf holds output not yet stored
	err   error
}

// newSink returns an empty sink storing id's output under key.
func (d *Dispatcher) newSink(ctx context.Context, id execution.ID, key execution.Lease) *logSink {
	return &logSink{
		limit: d.maxLogBytes,
		store: func(seq int64, data []byte, keepFrom int64) error {
			ctx, cancel := context.WithTimeout(ctx, recordTimeout)
			defer cancel()

			return d.executions.AppendLog(ctx, id, seq, data, keepFrom, key)
		},
		read: func(fromSeq int64, each func(seq int64, data []byte) error) error {
			return d.executions.ReadLog(ctx, id, fromSeq, each)
		},
	}
}

// resume picks up the output already stored: its first chunk is where the
// output now starts, and a last chunk not yet full is the one filling.
func (s *logSink) resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	first := true

	return s.read(0, func(seq int64, data []byte) error {
		if first {
			s.keep, first = seq, false
		}

		s.seq, s.buf = seq, append(s.buf[:0], data...)

		if len(s.buf) == execution.LogChunk {
			s.seq, s.buf = seq+1, s.buf[:0]
		}

		s.end = s.seq*execution.LogChunk + int64(len(s.buf))

		return nil
	})
}

// Write appends p, storing every chunk it fills.
func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	written := 0

	for len(p) > 0 {
		if s.err != nil {
			return written, s.err
		}

		if s.buf == nil {
			s.buf = make([]byte, 0, execution.LogChunk)
		}

		take := min(execution.LogChunk-len(s.buf), len(p))
		s.buf = append(s.buf, p[:take]...)
		s.end += int64(take)
		s.dirty = true
		p = p[take:]
		written += take

		if len(s.buf) == execution.LogChunk {
			if err := s.flushLocked(); err != nil {
				return written, err
			}
		}
	}

	return written, nil
}

// StartsAt places what is written next at offset, the provider having dropped
// what came before it, which the log then drops too. A node drops whole
// chunks, so offset must begin one, at or past the log's end.
func (s *logSink) StartsAt(offset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if offset%execution.LogChunk != 0 || offset < s.end {
		return fmt.Errorf("output cannot be placed at byte %d after %d bytes", offset, s.end)
	}

	s.buf, s.dirty = s.buf[:0], true
	s.seq, s.end, s.keep = offset/execution.LogChunk, offset, offset/execution.LogChunk

	return nil
}

// Len is where what is written next begins.
func (s *logSink) Len() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.end
}

// start is where what the log holds begins.
func (s *logSink) start() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.keep * execution.LogChunk
}

// WriteTo writes what the log holds to w, after storing what has not been, so
// the store holds all of it.
func (s *logSink) WriteTo(w io.Writer) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.flushLocked(); err != nil {
		return 0, err
	}

	var written int64

	err := s.read(s.keep, func(_ int64, data []byte) error {
		n, err := w.Write(data)
		written += int64(n)

		return err
	})

	return written, err
}

// flush stores what has arrived since the last store write, if anything has.
func (s *logSink) flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.flushLocked()
}

// failed is the store write the lease refused, if one was.
func (s *logSink) failed() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.err
}

// flushEvery stores what has arrived every flushInterval until ctx ends. A
// failed write is left for the next write to report.
func (s *logSink) flushEvery(ctx context.Context, d *Dispatcher, id execution.ID) {
	defer panics.Catch(ctx, d.logger, "storing the output of "+id.String(), nil)

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.flush()
		}
	}
}

// flushLocked stores the chunk filling, moving on to the next once it is full.
// The caller holds mu.
func (s *logSink) flushLocked() error {
	if s.err != nil || !s.dirty {
		return s.err
	}

	s.keep = max(s.keep, execution.KeepFrom(s.end, s.limit))

	if err := s.store(s.seq, s.buf, s.keep); err != nil {
		if errors.Is(err, execution.ErrStale) {
			s.err = err
		}

		return err
	}

	s.dirty = false

	if len(s.buf) == execution.LogChunk {
		s.seq, s.buf = s.seq+1, s.buf[:0]
	}

	return nil
}
