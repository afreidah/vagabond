// -------------------------------------------------------------------------------
// Following Output
//
// Author: Alex Freidah
//
// Sends an execution's stored output to a client, and for a request that
// follows, what is stored after, as the dispatch holding the execution stores
// it. Any server answers, since every one reads the same store; none opens the
// provider's own stream. A follow ends with the output, once a read made after
// the record was terminal is sent, since the terminal state is written only
// after the last of it.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// defaultFollowPoll is how often a followed execution's record and output are
// read again.
const defaultFollowPoll = time.Second

// errMoved ends a response whose next output was dropped past the ceiling after
// the body began, so the client asks again from where it got to and is told
// where the output now begins.
var errMoved = errors.New("the output to send next was dropped past the ceiling")

// -------------------------------------------------------------------------
// SENDING
// -------------------------------------------------------------------------

// sendLogs writes rec's stored output to out from out.next on, and when follow
// is set what is stored after, reading the record and output again every
// followPoll, until a read made once the record was terminal is sent, the
// client goes, or the server shuts down.
func (s *Server) sendLogs(ctx context.Context, rec *execution.Record, out *logsResponse, follow bool) error {
	for {
		complete := rec.Terminal()

		err := s.executions.ReadLog(ctx, rec.ID, out.next/execution.LogChunk, execution.Place(out.next, out))
		if err != nil || complete || !follow {
			return err
		}

		select {
		case <-ctx.Done():
			return nil
		case <-s.stopping:
			return nil
		case <-time.After(s.followPoll):
		}

		// A record that cannot be read is tried again; the client going ends this
		if next, err := s.executions.Get(ctx, rec.ID); err == nil {
			rec = next
		}
	}
}

// endStreams ends every followed output as shutdown begins. Safe to call more
// than once.
func (s *Server) endStreams() {
	s.stopOnce.Do(func() { close(s.stopping) })
}

// -------------------------------------------------------------------------
// RESPONSE
// -------------------------------------------------------------------------

// logsResponse is a logs response being written, each write sent to the
// client at once. next is where in the output the next write begins. Output
// placed past a gap before the body has begun moves the header saying where it
// begins; after, it ends the response with errMoved.
type logsResponse struct {
	flushing
	header http.Header
	next   int64
}

// newLogsResponse begins a response of output from byte offset on.
func newLogsResponse(w http.ResponseWriter, offset int64) *logsResponse {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set(api.LogsStartHeader, strconv.FormatInt(offset, 10))

	return &logsResponse{w: w, rc: http.NewResponseController(w), header: w.Header(), next: offset}
}

// Write sends p, unless it is empty, which would begin the body for nothing.
func (l *logsResponse) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	n, err := l.flushing.Write(p)
	l.next += int64(n)

	return n, err
}

// StartsAt places what is written next at offset, the output before it
// dropped.
func (l *logsResponse) StartsAt(offset int64) error {
	if l.wrote {
		return errMoved
	}

	l.header.Set(api.LogsStartHeader, strconv.FormatInt(offset, 10))
	l.next = offset

	return nil
}

// -------------------------------------------------------------------------
// FLUSHING WRITER
// -------------------------------------------------------------------------

// flushing sends each write to the client as soon as it is made, and records
// that the response has begun.
type flushing struct {
	w     io.Writer
	rc    *http.ResponseController
	wrote bool
}

// Write writes p and flushes it.
func (f *flushing) Write(p []byte) (int, error) {
	f.wrote = true

	n, err := f.w.Write(p)
	if err != nil {
		return n, err
	}

	return n, f.rc.Flush()
}
