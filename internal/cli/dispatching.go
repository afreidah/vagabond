// -------------------------------------------------------------------------------
// Following a Run
//
// Author: Alex Freidah
//
// job run and job dispatch start a run on the server and follow it here until
// it ends. Progress goes to stderr and each task's output to stdout, followed
// from the server's copy as the server stores it, from where it got to after
// any interruption, until the task has ended and its output is whole.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/backoff"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// pollInterval is how often a run is read back while it runs.
const pollInterval = time.Second

// reconnectDelay is how long a follow waits before asking again after a
// response that broke, or that ended while its execution still ran. A test
// shortens it.
var reconnectDelay = time.Second

// finalTries is how many reads of an ended execution's output may break before
// the follow gives up on the rest of it.
const finalTries = 3

// cancelTimeout bounds asking the server to stop a run after an interrupt.
const cancelTimeout = 30 * time.Second

// followRetries is how a command following a run rides out a server that
// stops answering, a redeploy or a lookup that fails. A test shortens it.
var followRetries = api.DefaultRetries

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
// code for the whole. An interrupt stops the run on the server; a server that
// stays unreachable past the retries ends the follow, not the run.
func (m *Meta) follow(ctx context.Context, client *api.Client, started *api.DispatchResponse, noLogs bool) int {
	m.Ui.Error(fmt.Sprintf("==> dispatch %s of %q", started.DispatchID, started.Job))

	f := m.newFollower(client, noLogs)
	reads := m.retrying(client)

	var last *api.Dispatch

	err := poll(ctx, func(ctx context.Context) (*api.Dispatch, error) {
		return reads.DispatchStatus(ctx, started.DispatchID)
	}, func(d *api.Dispatch) bool {
		last = d
		f.report(ctx, d)

		return d.State != dispatchRunning
	})

	switch {
	case errors.Is(err, context.Canceled):
		return f.interrupted(started.Job, last)
	case err != nil:
		return m.lost(err, "dispatch", started.DispatchID)
	}

	return f.finished(last)
}

// poll reads a run every pollInterval, handing each read to step until step
// reports it over. A read that fails, after the client's own retries, ends it
// with that error; an interrupt ends it with context.Canceled.
func poll[T any](ctx context.Context, read func(ctx context.Context) (T, error), step func(T) bool) error {
	for {
		got, err := read(ctx)
		if err != nil {
			return err
		}

		if step(got) {
			return nil
		}

		if err := backoff.Sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// retrying returns client retrying its reads as followRetries says, warning
// once for each read the server leaves unanswered.
func (m *Meta) retrying(client *api.Client) *api.Client {
	r := followRetries
	r.Retrying = func() {
		m.Ui.Warn(fmt.Sprintf("Lost contact with the server at %s, retrying.", client.Address()))
	}

	return client.Retrying(r)
}

// lost reports a follow of the dispatch or execution id that ended without its
// end, and returns the exit code. When the server stopped answering, the run
// may carry on without the follow, so it says so and which status reads it.
func (m *Meta) lost(err error, kind, id string) int {
	code := m.apiFailure(err)

	if api.Unanswered(err) {
		m.Ui.Error(fmt.Sprintf("%s%s %s may still be running. Check it with vagabond %s status %s.",
			strings.ToUpper(kind[:1]), kind[1:], id, kind, id))
	}

	return code
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

		line := fmt.Sprintf("==> %s %s on %s", e.Task, e.State, e.Provider)
		if e.Attempt > 1 {
			line += fmt.Sprintf(" (attempt %d)", e.Attempt)
		}

		if e.Failure != "" {
			line += fmt.Sprintf(" (%s failure)", e.Failure)
		}

		f.Ui.Error(line + f.introduce(e))
		f.states[e.ID] = e.State
	}
}

// introduce is " (execution <id>)" when no line has reported e yet, so a run
// names each execution once, and empty after.
func (f *follower) introduce(e *api.Execution) string {
	if _, seen := f.states[e.ID]; seen {
		return ""
	}

	return " (execution " + e.ID + ")"
}

// result waits for the rest of a finished execution's output, unless -no-logs,
// then prints its verdict with the platform's reason when it gave one, once.
func (f *follower) result(ctx context.Context, e *api.Execution) {
	if f.reported[e.ID] {
		return
	}

	f.reported[e.ID] = true

	if !f.noLogs {
		f.rest(ctx, e)
	}

	line := fmt.Sprintf("==> %s %s on %s in %s", e.Task, verdict(e), e.Provider, e.Duration) + f.introduce(e)
	if e.Reason != "" {
		line += ": " + e.Reason
	}

	f.Ui.Error(line)
}

// -------------------------------------------------------------------------
// LIVE OUTPUT
// -------------------------------------------------------------------------

// live is one execution's output, followed to stdout until it is whole. err
// is why the follow gave up before then, read once done has closed.
type live struct {
	out      *output
	complete chan struct{} // closed once the execution has ended, its output whole
	end      func()        // closes complete; safe to repeat
	done     chan struct{} // closed once the follow has stopped
	err      error
}

// watch follows a submitted execution's output, once, unless -no-logs, and
// tells the follow once the execution has ended, its output then whole.
func (f *follower) watch(ctx context.Context, e *api.Execution) {
	l, followed := f.live[e.ID]

	switch {
	case e.Ended != nil:
		if followed {
			l.end()
		}
	case f.noLogs, followed, e.State == executionPending:
	default:
		f.live[e.ID] = f.start(ctx, e)
	}
}

// start follows e's output to stdout, asking again from where it got to after
// a response that ends before the output is whole. One that ended cleanly
// waits for the execution to end, the likely reason, or reconnectDelay, since
// a server shutting down ends one too; one that broke waits reconnectDelay,
// and once the output is whole gives up after finalTries.
func (f *follower) start(ctx context.Context, e *api.Execution) *live {
	complete := make(chan struct{})

	l := &live{
		out:      f.output(),
		complete: complete,
		end:      sync.OnceFunc(func() { close(complete) }),
		done:     make(chan struct{}),
	}

	if e.Ended != nil {
		l.end()
	}

	go func() {
		defer close(l.done)

		l.err = l.follow(ctx, f.client, e.ID)
	}()

	return l
}

// follow asks for id's output from where it got to until a response asked
// for once the output was whole has ended, returning why it gave up first.
func (l *live) follow(ctx context.Context, client *api.Client, id string) error {
	for broken := 0; ; {
		whole := closed(l.complete)
		err := client.FollowLogs(ctx, id, l.out.position(), l.out)

		switch {
		case whole && err == nil:
			return nil
		case whole:
			if broken++; broken >= finalTries {
				return err
			}
		}

		if !l.pause(ctx, err == nil) {
			return nil
		}
	}
}

// pause waits before asking again: until the execution ends or reconnectDelay
// after a response that ended cleanly, reconnectDelay after one that broke. It
// reports false once ctx has ended.
func (l *live) pause(ctx context.Context, clean bool) bool {
	var ending <-chan struct{}
	if clean {
		ending = l.complete
	}

	select {
	case <-ctx.Done():
		return false
	case <-ending:
	case <-time.After(reconnectDelay):
	}

	return true
}

// rest waits until a finished execution's output has all been printed,
// following it from the start when it was not followed while it ran, then
// closes it.
func (f *follower) rest(ctx context.Context, e *api.Execution) {
	l, ok := f.live[e.ID]
	if !ok {
		l = f.start(ctx, e)
		f.live[e.ID] = l
	}

	// The follow ends with the output, so all of it precedes the verdict
	l.end()
	<-l.done

	f.closeOutput(e, l.out, l.err)
}

// closeOutput ends e's printed output on a line of its own and warns of
// output that is not there: unread, as the read failed with err, or never
// given by the provider. Output dropped past the server's ceiling was warned
// of where it was skipped.
func (f *follower) closeOutput(e *api.Execution, out *output, err error) {
	if err != nil {
		f.Ui.Warn(fmt.Sprintf("Could not read the output of %s: %s", e.ID, err))
	}

	if out.endsMidLine() {
		_, _ = out.Write([]byte("\n"))
	}

	if e.LogsTruncated {
		f.Ui.Warn("The provider gave only part of the output.")
	}
}

// output is one execution's share of stdout, under one lock every output
// shares. pos is where in the execution's output the next write continues,
// which a follow asks again from.
type output struct {
	mu    *sync.Mutex
	w     io.Writer
	warn  func(string)
	pos   int64
	wrote bool
	last  byte
}

// output returns an execution's share of stdout, nothing written yet.
func (f *follower) output() *output {
	return &output{mu: &f.stdout, w: f.Stdout, warn: f.Ui.Warn}
}

// Write writes p to stdout, counting it.
func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	n, err := o.w.Write(p)
	o.pos += int64(n)

	if n > 0 {
		o.wrote, o.last = true, p[n-1]
	}

	return n, err
}

// StartsAt continues the output at offset, warning of the bytes before it,
// which the server dropped before they could be printed.
func (o *output) StartsAt(offset int64) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if dropped := offset - o.pos; dropped > 0 {
		o.warn(fmt.Sprintf("%d bytes of output were not kept: only the end of long output is kept.", dropped))
		o.pos = offset
	}

	return nil
}

// position is where in the execution's output the next write continues.
func (o *output) position() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.pos
}

// endsMidLine reports whether output was written and its last byte did not
// end a line.
func (o *output) endsMidLine() bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.wrote && o.last != '\n'
}

// closed reports whether ch has closed.
func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
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
