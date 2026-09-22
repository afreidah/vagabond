// -------------------------------------------------------------------------------
// Live Output
//
// Author: Alex Freidah
//
// Runs a provider's log stream alongside the poll loop, for providers that
// offer one. The stream is a view, so nothing here can fail an execution: a
// broken stream loses the picture, not the work.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// stream is a running log stream, and the handle used to stop it.
//
// wrote counts bytes rather than recording that a stream was opened, because
// the caller uses it to decide whether the output still needs printing. A
// stream that opened and delivered nothing — a short job finishing before
// Cloud Logging ingests anything — must not suppress the result's own copy.
type stream struct {
	cancel context.CancelFunc
	done   sync.WaitGroup
	writer *countingWriter
}

// wrote reports whether anything reached the caller's writer.
func (s *stream) wrote() bool {
	if s == nil {
		return false
	}

	return s.writer.count.Load() > 0
}

// countingWriter records how much got through.
type countingWriter struct {
	w     io.Writer
	count atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.count.Add(int64(n))

	return n, err
}

// startStream begins streaming if this provider supports it and the caller
// asked for output.
//
// Returns nil when either is untrue, and stop handles a nil receiver, so the
// caller never branches on whether streaming happened.
func (d *Dispatcher) startStream(
	ctx context.Context, provider plugin.Provider, id execution.ID,
) *stream {
	if d.logs == nil {
		return nil
	}

	streamer, ok := provider.(plugin.LogStreamer)
	if !ok {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	s := &stream{cancel: cancel, writer: &countingWriter{w: d.logs}}

	s.done.Add(1)

	go func() {
		defer s.done.Done()

		// Deliberately discarded. The execution is what matters, and a caller
		// who stopped watching is the common reason this returns at all.
		_ = streamer.StreamLogs(ctx, id, s.writer)
	}()

	return s
}

// stop ends the stream and waits for it to finish writing.
//
// Waiting matters: without it the last lines of a build race the result the
// caller is about to print, and land underneath it.
func (s *stream) stop() {
	if s == nil {
		return
	}

	s.cancel()
	s.done.Wait()
}

// settle gives the stream a moment to deliver what it has before stopping it.
//
// Cloud Logging is seconds behind the container, so an execution that ends
// promptly ends before its own last lines have been ingested. Without this a
// short job streams nothing at all.
func (d *Dispatcher) settle(ctx context.Context, s *stream) {
	if s == nil {
		return
	}

	// Ignored: the caller having given up is a reason to stop waiting for
	// trailing output, not to skip stopping the stream.
	_ = d.sleep(ctx, d.linger)

	s.stop()
}

// -------------------------------------------------------------------------
// PROGRESS
// -------------------------------------------------------------------------

// Event is something worth telling the caller about while a task runs.
type Event struct {
	Task     string
	Provider string
	ID       execution.ID
	State    execution.State
	Attempt  int
}

// report sends an event, if anyone is listening.
func (d *Dispatcher) report(e Event) {
	if d.progress == nil {
		return
	}

	d.progress(e)
}

// writerOrNil keeps a nil io.Writer interface from wrapping a nil pointer.
func writerOrNil(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}

	return w
}
