// -------------------------------------------------------------------------------
// Log Streaming
//
// Author: Alex Freidah
//
// entries:tail, a bidirectional streaming method reachable over plain HTTP
// through gRPC transcoding. The request body is a JSON array because the body
// is a stream of requests, and the response is a JSON array that stays open
// rather than one object per line.
//
// A tail starts at what is being logged now, so a stream lists what was logged
// before it first, and drops the tail's copies of what the list wrote. The
// stream then starts at the first byte the task printed, the same bytes the
// stored copy holds, so an offset places it.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// bufferWindow is how long Cloud Logging holds entries to order them. Below
// about a second, stderr arrives before the stdout that preceded it.
const bufferWindow = "1s"

// tailBacklog is how many tailed entries wait while the backfill is listed.
// Cloud Logging drops entries a client does not read quickly enough, so the
// tail is read into this from the moment it opens.
const tailBacklog = 10_000

// -------------------------------------------------------------------------
// STREAMING
// -------------------------------------------------------------------------

// StreamEntries writes a running execution's output, the entries filter
// matches, to w from byte offset on, as it is logged. Output that cannot be
// placed, because the backfill failed or stopped at the page cap, is
// plugin.ErrNoResume.
func (a *API) StreamEntries(ctx context.Context, filter string, offset int64, w io.Writer) error {
	// Opened first, so nothing logged while the backfill is listed is missed
	resp, err := a.openTail(ctx, filter)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tail, done := backlog(ctx, resp.Body)

	// Cancelled first, since a backlog blocked on a full queue waits on ctx, not the body
	defer func() {
		cancel()
		_ = resp.Body.Close()
		<-done
	}()

	out := plugin.LogOutput(plugin.SkipWriter(w, offset))
	listed := make(map[entryKey]bool)

	var failedWrite error

	err = a.listEntries(ctx, filter, func(entry *logEntry) error {
		listed[entry.key()] = true

		failedWrite = writeEntry(out, entry)

		return failedWrite
	})

	// A failed write is the caller's; any other failure leaves the stream unplaced
	switch {
	case failedWrite != nil:
		return failedWrite
	case err != nil:
		return errors.Join(plugin.ErrNoResume, err)
	}

	for entry := range tail {
		if listed[entry.key()] {
			continue
		}

		if err := writeEntry(out, entry); err != nil {
			return err
		}
	}

	return nil
}

// openTail starts the stream of the entries filter matches, whose response
// never ends.
func (a *API) openTail(ctx context.Context, filter string) (*http.Response, error) {
	query := a.logQuery(filter)
	query["bufferWindow"] = bufferWindow

	// A bare object is rejected as "Invalid value (Object)"
	return a.Open(ctx, http.MethodPost, a.LogsURL+"/v2/entries:tail", []map[string]any{query})
}

// backlog reads the tail into a channel from now on, closing it when the tail
// ends or ctx does, and done once it has stopped reading.
func backlog(ctx context.Context, body io.Reader) (<-chan *logEntry, <-chan struct{}) {
	tail := make(chan *logEntry, tailBacklog)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer close(tail)

		_ = decodeTail(body, func(entry *logEntry) error {
			select {
			case tail <- entry:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	return tail, done
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// decodeTail calls each on every entry of the open array as it arrives, until
// the array ends or each fails. A read failure ends it quietly: no way a
// stream stops fails the work.
func decodeTail(body io.Reader, each func(*logEntry) error) error {
	dec := json.NewDecoder(body)

	// The opening bracket of the response array.
	if _, err := dec.Token(); err != nil {
		return nil
	}

	for dec.More() {
		var chunk tailChunk

		if err := dec.Decode(&chunk); err != nil {
			return nil
		}

		for i := range chunk.Entries {
			if err := each(&chunk.Entries[i]); err != nil {
				return err
			}
		}
	}

	return nil
}

// tailChunk is one TailLogEntriesResponse off the stream.
type tailChunk struct {
	Entries []logEntry `json:"entries"`
}
