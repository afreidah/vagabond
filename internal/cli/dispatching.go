// -------------------------------------------------------------------------------
// Following a Run
//
// Author: Alex Freidah
//
// job run and job dispatch start a run on the server and wait for it here,
// reading it back until it ends. Progress goes to stderr and each task's
// output to stdout, so redirecting stdout captures the build alone. Output is
// printed when each task finishes; live streaming arrives with the server's
// event stream.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/api"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// pollInterval is how often a run is read back while it runs.
const pollInterval = time.Second

// cancelTimeout bounds asking the server to stop a run after an interrupt.
const cancelTimeout = 30 * time.Second

// Dispatch states, as the API reports them.
const (
	dispatchRunning   = "running"
	dispatchSucceeded = "succeeded"
	dispatchFailed    = "failed"
)

// executionFailed is an execution's failed state, as the API reports it.
const executionFailed = "failed"

// -------------------------------------------------------------------------
// FOLLOWING
// -------------------------------------------------------------------------

// follower remembers what has been reported of one run, so each state change
// and each finished task is reported once.
type follower struct {
	*Meta
	client   *api.Client
	noLogs   bool
	states   map[string]string
	reported map[string]bool
}

// follow waits for a started run, reporting as it goes, and returns the exit
// code for the whole. An interrupt stops the run on the server.
func (m *Meta) follow(ctx context.Context, client *api.Client, started *api.DispatchResponse, noLogs bool) int {
	m.Ui.Error(fmt.Sprintf("==> dispatch %s of %q", started.DispatchID, started.Job))

	f := &follower{
		Meta: m, client: client, noLogs: noLogs,
		states: make(map[string]string), reported: make(map[string]bool),
	}

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

// report prints every execution's state change, and each one's output and
// verdict once it has a result.
func (f *follower) report(ctx context.Context, d *api.Dispatch) {
	for i := range d.Executions {
		e := &d.Executions[i]

		if e.HasResult {
			f.result(ctx, e)

			continue
		}

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

// result prints a finished execution's output, unless -no-logs, and its
// verdict, once.
func (f *follower) result(ctx context.Context, e *api.Execution) {
	if f.reported[e.ID] {
		return
	}

	f.reported[e.ID] = true

	if !f.noLogs {
		logs, err := f.client.Logs(ctx, e.ID)

		switch {
		case err != nil:
			f.Ui.Warn(fmt.Sprintf("Could not read the output of %s: %s", e.ID, err))
		case len(logs) > 0:
			f.Ui.Output(strings.TrimRight(string(logs), "\n"))

			if e.LogsTruncated {
				f.Ui.Warn("Output was truncated.")
			}
		}
	}

	f.Ui.Error(fmt.Sprintf("==> %s %s on %s in %s", e.Task, verdict(e), e.Provider, e.Duration))
}

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
