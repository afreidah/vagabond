// -------------------------------------------------------------------------------
// Live Output
//
// Author: Alex Freidah
//
// Streams a running execution's output to a client from the provider running
// it. The stream is a view: it never touches the execution, and a broken one
// only ends the response. It ends once the record has ended and the linger has
// passed, since some providers' logs trail the work by seconds.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

const (
	defaultFollowPoll   = time.Second     // how often a followed execution's record is re-read
	defaultFollowLinger = 5 * time.Second // how long a stream outlives its execution
)

// streamer is the provider streaming rec's output, when the request asked to
// follow and rec has not ended.
func (s *Server) streamer(r *http.Request, rec *execution.Record) (plugin.LogStreamer, bool) {
	if r.URL.Query().Get("follow") != "true" || rec.Terminal() {
		return nil, false
	}

	provider, ok := s.registry.Provider(rec.Provider)
	if !ok {
		return nil, false
	}

	streamer, ok := provider.(plugin.LogStreamer)

	return streamer, ok
}

// follow copies id's output from byte offset on, from streamer to w as it is
// written, until the execution has ended and the linger passed, the client
// goes, or the server shuts down. A provider that cannot resume from offset is
// answered 416, so the client finishes from the stored output.
func (s *Server) follow(
	ctx context.Context, w http.ResponseWriter, streamer plugin.LogStreamer, id execution.ID, offset int64,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go s.endFollow(ctx, cancel, id)

	// Headers go out with the first write, so a stream refused before then can
	// still be answered with a status.
	out := &flushing{w: w, rc: http.NewResponseController(w)}

	err := streamer.StreamLogs(ctx, id, offset, out)

	switch {
	case errors.Is(err, plugin.ErrNoResume) && !out.wrote:
		write(w, http.StatusRequestedRangeNotSatisfiable, api.Error{Error: err.Error()})
	case err != nil && ctx.Err() == nil:
		s.logger.WarnContext(ctx, "log stream ended early", "execution", id, "error", err)
	}
}

// endFollow re-reads id's record until it has ended, then calls stop once the
// linger has passed. A shutdown stops it at once.
func (s *Server) endFollow(ctx context.Context, stop context.CancelFunc, id execution.ID) {
	ended := false

	for {
		wait := s.followPoll
		if ended {
			wait = s.followLinger
		}

		select {
		case <-ctx.Done():
			return
		case <-s.stopping:
			stop()

			return
		case <-time.After(wait):
		}

		if ended {
			stop()

			return
		}

		// A record that cannot be read is retried; the client going ends this.
		rec, err := s.executions.Get(ctx, id)
		ended = err == nil && rec.Terminal()
	}
}

// endStreams ends every live output stream as shutdown begins. Safe to call
// more than once.
func (s *Server) endStreams() {
	s.stopOnce.Do(func() { close(s.stopping) })
}

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
