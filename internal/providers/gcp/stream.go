// -------------------------------------------------------------------------------
// Capturing Entries
//
// Author: Alex Freidah
//
// A run's output as Cloud Logging entries, captured while it runs. A tail,
// entries:tail, delivers entries as they are logged; it is a bidirectional
// streaming method reachable over plain HTTP through gRPC transcoding, so the
// request body is a JSON array of requests and the response a JSON array that
// stays open. A tail starts at the present, can leave entries out, and can
// end, so every time one opens, and once the run is over, a catch-up lists
// the run's entries and appends each whose line the log does not hold. Lines
// are matched by their text and counted, so a capture another server began is
// resumed from the log alone. A line found late is appended where it is found,
// out of order.
// -------------------------------------------------------------------------------

package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// bufferWindow is how long Cloud Logging holds a tail's entries to deliver
// them in timestamp order. Within about a second, stderr arrives before the
// stdout printed ahead of it; an entry later than the window arrives on its
// own, out of order.
const bufferWindow = "5s"

// tailBacklog is how many tailed entries wait while a catch-up lists. Cloud
// Logging drops entries a client does not read quickly enough, so the tail is
// read into this from the moment it opens.
const tailBacklog = 10_000

// settleWait is how long a capture waits once the run is over for entries
// still on their way, before its last catch-up.
const settleWait = 5 * time.Second

// errTailEnded reports a tail that ended its response without saying why.
var errTailEnded = errors.New("the tail ended without an error from Cloud Logging")

// -------------------------------------------------------------------------
// CAPTURE
// -------------------------------------------------------------------------

// CaptureEntries copies a run's output, the entries filter matches, into log
// as they are logged, from what log holds on. It returns nil once ended has
// closed and, Settle later, a last catch-up has appended what log lacked. A
// tail that ends before then is an error saying why, for the capture to be
// made again.
func (a *API) CaptureEntries(ctx context.Context, filter string, log execution.Log, ended <-chan struct{}) error {
	if !plugin.Ended(ended) {
		if err := a.follow(ctx, filter, log, ended); err != nil {
			return err
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(a.Settle):
	}

	_, err := a.catchUp(ctx, filter, log)

	return err
}

// follow tails the entries filter matches into log until ended closes,
// catching up once the tail is open, and skipping the tail's copies of what
// the catch-up listed. A tail that ends first is an error saying why.
func (a *API) follow(ctx context.Context, filter string, log execution.Log, ended <-chan struct{}) error {
	// Opened first, so nothing logged while the catch-up lists is missed
	resp, err := a.openTail(ctx, filter)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var why error

	tail, done := backlog(ctx, resp.Body, &why)

	// Cancelled first, since a backlog blocked on a full queue waits on ctx, not the body
	defer func() {
		cancel()
		_ = resp.Body.Close()
		<-done
	}()

	listed, err := a.catchUp(ctx, filter, log)
	if err != nil {
		return err
	}

	out := plugin.LogOutput(log)

	for {
		select {
		case <-ended:
			return nil

		case entry, ok := <-tail:
			if !ok {
				return why
			}

			if listed[entry.key()] {
				continue
			}

			if err := writeEntry(out, entry); err != nil {
				return err
			}
		}
	}
}

// openTail starts the stream of the entries filter matches, whose response
// never ends of its own accord.
func (a *API) openTail(ctx context.Context, filter string) (*http.Response, error) {
	query := a.logQuery(filter)
	query["bufferWindow"] = bufferWindow

	// A bare object is rejected as "Invalid value (Object)"
	return a.Open(ctx, http.MethodPost, a.LogsURL+"/v2/entries:tail", []map[string]any{query})
}

// backlog reads the tail into a channel from now on, closing it when the tail
// ends or ctx does, why it ended set before it closes, and done once it has
// stopped reading.
func backlog(ctx context.Context, body io.Reader, why *error) (<-chan *logEntry, <-chan struct{}) {
	tail := make(chan *logEntry, tailBacklog)
	done := make(chan struct{})

	go func() {
		defer close(done)
		defer close(tail)

		*why = decodeTail(body, func(entry *logEntry) error {
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
// CATCHING UP
// -------------------------------------------------------------------------

// catchUp lists the entries filter matches and appends to log each whose line
// log does not hold, a line held twice counting as two, returning the key of
// every entry listed. A log ending part way through a line, as a server that
// stopped mid-write leaves one, is ended first, so no line is joined to it.
func (a *API) catchUp(ctx context.Context, filter string, log execution.Log) (map[entryKey]bool, error) {
	var held bytes.Buffer

	if _, err := log.WriteTo(&held); err != nil {
		return nil, plugin.Internal(fmt.Errorf("reading the log back: %w", err))
	}

	lines, broken := countLines(held.String())
	out := plugin.LogOutput(log)
	listed := make(map[entryKey]bool)

	err := a.listEntries(ctx, filter, func(entry *logEntry) error {
		listed[entry.key()] = true

		if entry.TextPayload == "" || lines.take(entry.TextPayload) {
			return nil
		}

		if broken {
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}

			broken = false
		}

		return writeEntry(out, entry)
	})
	if errors.Is(err, errPageCap) {
		err = plugin.Infrastructure(err)
	}

	return listed, err
}

// lineCount is how many times a log holds each line, by its text.
type lineCount map[string]int

// countLines counts the lines of text, and reports whether its last one is
// cut short of its end.
func countLines(text string) (lineCount, bool) {
	counts := make(lineCount)

	if text == "" {
		return counts, false
	}

	for line := range strings.SplitSeq(strings.TrimSuffix(text, "\n"), "\n") {
		counts[line]++
	}

	return counts, !strings.HasSuffix(text, "\n")
}

// take reports whether every line of text is still counted, and counts them
// off when it is.
func (c lineCount) take(text string) bool {
	need := make(lineCount)
	for line := range strings.SplitSeq(text, "\n") {
		need[line]++
	}

	for line, n := range need {
		if c[line] < n {
			return false
		}
	}

	for line, n := range need {
		c[line] -= n
	}

	return true
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// decodeTail calls each on every entry of the open array as it arrives, until
// the array ends, Google sends the error that ends it, a read fails, or each
// does. It never returns nil, since a tail never ends of its own accord.
func decodeTail(body io.Reader, each func(*logEntry) error) error {
	dec := json.NewDecoder(body)

	// The opening bracket of the response array
	if _, err := dec.Token(); err != nil {
		return plugin.Infrastructure(fmt.Errorf("reading the tail: %w", err))
	}

	for dec.More() {
		var chunk tailChunk

		if err := dec.Decode(&chunk); err != nil {
			return plugin.Infrastructure(fmt.Errorf("reading the tail: %w", err))
		}

		if chunk.Error != nil {
			return plugin.Infrastructure(fmt.Errorf("the tail was ended by Cloud Logging: %s %s",
				chunk.Error.Status, chunk.Error.Message))
		}

		for i := range chunk.Entries {
			if err := each(&chunk.Entries[i]); err != nil {
				return err
			}
		}
	}

	return plugin.Infrastructure(errTailEnded)
}

// tailChunk is one element of the tail's response: a TailLogEntriesResponse,
// or the status gRPC transcoding sends as the last element of a stream that
// failed after it began.
type tailChunk struct {
	Entries []logEntry `json:"entries"`
	Error   *struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}
