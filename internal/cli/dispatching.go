// -------------------------------------------------------------------------------
// Following a Run
//
// Author: Alex Freidah
//
// job run and job dispatch start a run on the server and follow it here until
// it ends. Progress goes to stderr and each task's output to stdout. Output
// streams as each task runs, and whatever did not stream is read from the
// stored copy once the task finishes.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/api"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// pollInterval is how often a run is read back while it runs.
const pollInterval = time.Second

// reconnectDelay is how long a follow waits before reopening a stream that
// ended while its execution still ran.
const reconnectDelay = time.Second

// cancelTimeout bounds asking the server to stop a run after an interrupt.
const cancelTimeout = 30 * time.Second

// Dispatch states, as the API reports them.
const (
	dispatchRunning   = "running"
	dispatchSucceeded = "succeeded"
	dispatchFailed    = "failed"
)

// Execution states, as the API reports them. A pending execution has not been
// submitted, so it has nothing to stream yet.
const (
	executionPending = "pending"
	executionFailed  = "failed"
)

// -------------------------------------------------------------------------
// FOLLOWING
// -------------------------------------------------------------------------

// follower remembers what has been reported of one run, so each state change
// and each finished task is reported once, and the output it is following.
type follower struct {
	*Meta
	client   *api.Client
	noLogs   bool
	states   map[string]string
	reported map[string]bool
	live     map[string]*live
	stdout   sync.Mutex // shared by every execution's output
}

// newFollower starts an empty account of a run.
func (m *Meta) newFollower(client *api.Client, noLogs bool) *follower {
	return &follower{
		Meta: m, client: client, noLogs: noLogs,
		states: make(map[string]string), reported: make(map[string]bool), live: make(map[string]*live),
	}
}

// follow waits for a started run, reporting as it goes, and returns the exit
// code for the whole. An interrupt stops the run on the server.
func (m *Meta) follow(ctx context.Context, client *api.Client, started *api.DispatchResponse, noLogs bool) int {
	m.Ui.Error(fmt.Sprintf("==> dispatch %s of %q", started.DispatchID, started.Job))

	f := m.newFollower(client, noLogs)

	var last *api.Dispatch

	for {
		d, err := client.DispatchStatus(ctx, started.DispatchID)

		switch {
		case errors.Is(err, context.Canceled):
			return f.interrupted(started.Job, last)
		case err != nil:
			return m.apiFailure(err)
		}

		last = d
		f.report(ctx, d)

		if d.State != dispatchRunning {
			return f.finished(d)
		}

		select {
		case <-ctx.Done():
			return f.interrupted(started.Job, last)
		case <-time.After(pollInterval):
		}
	}
}

// report prints every execution's state change, follows each one's output
// while it runs, and prints its verdict once it has a result.
func (f *follower) report(ctx context.Context, d *api.Dispatch) {
	for i := range d.Executions {
		e := &d.Executions[i]

		if e.HasResult {
			f.result(ctx, e)

			continue
		}

		f.watch(ctx, e)

		if f.states[e.ID] == e.State {
			continue
		}

		f.states[e.ID] = e.State

		line := fmt.Sprintf("==> %s %s on %s", e.Task, e.State, e.Provider)
		if e.Attempt > 1 {
			line += fmt.Sprintf(" (attempt %d)", e.Attempt)
		}

		if e.Failure != "" {
			line += fmt.Sprintf(" (%s failure)", e.Failure)
		}

		f.Ui.Error(line)
	}
}

// result prints the rest of a finished execution's output, unless -no-logs,
// and its verdict with the platform's reason when it gave one, once.
func (f *follower) result(ctx context.Context, e *api.Execution) {
	if f.reported[e.ID] {
		return
	}

	f.reported[e.ID] = true

	if !f.noLogs {
		f.rest(ctx, e)
	}

	line := fmt.Sprintf("==> %s %s on %s in %s", e.Task, verdict(e), e.Provider, e.Duration)
	if e.Reason != "" {
		line += ": " + e.Reason
	}

	f.Ui.Error(line)
}

// -------------------------------------------------------------------------
// LIVE OUTPUT
// -------------------------------------------------------------------------

// live is one execution's output, followed to stdout while it runs.
type live struct {
	out   *output
	end   func()        // tells the follow its execution is over; safe to repeat
	done  chan struct{} // closed once the follow has stopped
	broke bool          // a stream failed rather than ended; read after done
}

// watch follows a submitted execution's output, once, unless -no-logs, and
// stops following one that ended with no result, since nothing more can come.
func (f *follower) watch(ctx context.Context, e *api.Execution) {
	l, followed := f.live[e.ID]

	switch {
	case e.Ended != nil:
		if followed {
			l.end()
		}
	case f.noLogs, followed, e.State == executionPending:
	default:
		f.live[e.ID] = f.start(ctx, e.ID)
	}
}

// start follows id's output to stdout, reopening the stream from what arrived
// when it ends early, and leaves the rest to the stored copy on a 416.
func (f *follower) start(ctx context.Context, id string) *live {
	ended := make(chan struct{})

	l := &live{
		out:  &output{mu: &f.stdout, w: f.Stdout},
		end:  sync.OnceFunc(func() { close(ended) }),
		done: make(chan struct{}),
	}

	go func() {
		defer close(l.done)

		for {
			err := f.client.FollowLogs(ctx, id, l.out.written(), l.out)
			if err != nil {
				l.broke = true
			}

			if failure, ok := errors.AsType[*api.ResponseError](err); ok &&
				failure.Status == http.StatusRequestedRangeNotSatisfiable {
				return
			}

			select {
			case <-ended:
				return
			case <-ctx.Done():
				return
			case <-time.After(reconnectDelay):
			}
		}
	}()

	return l
}

// rest prints what of a finished execution's stored output did not stream, and
// warns when truncation hides where a broken stream left off.
func (f *follower) rest(ctx context.Context, e *api.Execution) {
	out := &output{mu: &f.stdout, w: f.Stdout}
	broke := false

	if l, ok := f.live[e.ID]; ok {
		// The stream ends with the execution, so its tail precedes the verdict
		l.end()
		<-l.done

		out, broke = l.out, l.broke
	}

	streamed := out.written()

	logs, err := f.client.Logs(ctx, e.ID, streamed)
	if err != nil {
		f.Ui.Warn(fmt.Sprintf("Could not read the output of %s: %s", e.ID, err))

		return
	}

	_, _ = out.Write(logs)

	if out.written() > 0 && !out.endsLine() {
		_, _ = out.Write([]byte("\n"))
	}

	switch {
	case !e.LogsTruncated:
	case streamed == 0:
		f.Ui.Warn("Output was truncated.")
	case broke:
		f.Ui.Warn(fmt.Sprintf("Live output was interrupted. vagabond execution logs %s has its stored tail.", e.ID))
	}
}

// output is one execution's share of stdout. It counts what it wrote, where a
// reopened stream resumes, under one lock every output shares.
type output struct {
	mu   *sync.Mutex
	w    io.Writer
	n    int64
	last byte
}

// Write writes p to stdout and counts it.
func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	n, err := o.w.Write(p)

	o.n += int64(n)
	if n > 0 {
		o.last = p[n-1]
	}

	return n, err
}

// written is how many bytes have reached stdout.
func (o *output) written() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.n
}

// endsLine reports whether the last byte written ended a line.
func (o *output) endsLine() bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.last == '\n'
}

// -------------------------------------------------------------------------
// OUTCOME
// -------------------------------------------------------------------------

// finished reports how the run ended and returns its exit code: success, a
// task that failed, or a run that got no answer.
func (f *follower) finished(d *api.Dispatch) int {
	switch d.State {
	case dispatchSucceeded:
		return ExitSuccess

	case dispatchFailed:
		return ExitFailure

	default:
		f.Ui.Error(fmt.Sprintf("Job %q did not run: %s", d.Job, d.Error))
		f.Ui.Error("Run vagabond job plan to see where it can run.")

		return ExitNoCapacity
	}
}

// interrupted stops the run's unfinished execution on the server, which stops
// the whole run, and reports that the work did not finish.
func (f *follower) interrupted(job string, last *api.Dispatch) int {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()

	if last != nil {
		for i := range last.Executions {
			if e := &last.Executions[i]; !e.HasResult && e.Ended == nil {
				if _, err := f.client.Cancel(ctx, e.ID); err != nil {
					f.Ui.Warn(fmt.Sprintf("Could not stop %s: %s", e.ID, err))
				}
			}
		}
	}

	f.Ui.Error(fmt.Sprintf("Job %q was interrupted. The execution was stopped.", job))

	return ExitNoCapacity
}

// verdict renders how the task ended, which the provider's state decides,
// with its exit code when it failed and has one.
func verdict(e *api.Execution) string {
	if e.State == executionFailed && e.ExitCode != nil {
		return fmt.Sprintf("failed (exit %d)", *e.ExitCode)
	}

	return e.State
}
