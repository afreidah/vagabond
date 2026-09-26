// -------------------------------------------------------------------------------
// Dispatching and Reporting
//
// Author: Alex Freidah
//
// Shared by job run and job dispatch, so that a job run from a file and one
// dispatched by name report identically.
//
// Output is split across streams: the task's own output goes to stdout and
// everything Vagabond has to say goes to stderr, so that redirecting stdout
// captures the build and leaves the progress on the terminal.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// newDispatcher builds a dispatcher over the configured providers and stores,
// and first resolves reservations a killed run left behind: they hold quota
// this one may need. Failing to resolve them costs headroom, not correctness,
// so it warns.
func (m *Meta) newDispatcher(
	ctx context.Context, reg *registry.Registry, s *stores, noLogs bool,
) *dispatch.Dispatcher {
	opts := []dispatch.Option{dispatch.WithProgress(m.progress)}
	if !noLogs {
		opts = append(opts, dispatch.WithLogs(os.Stdout))
	}

	d := dispatch.New(reg, s.ledger, s.executions, opts...)

	if reaped, err := d.Reap(ctx); err != nil {
		m.Ui.Warn(fmt.Sprintf("Could not resolve abandoned quota reservations: %s", err))
	} else if reaped > 0 {
		m.Ui.Info(fmt.Sprintf("Resolved %d abandoned quota reservation(s).", reaped))
	}

	return d
}

// runJob dispatches one job and reports what happened.
func (m *Meta) runJob(
	ctx context.Context, d *dispatch.Dispatcher, origin dispatch.Origin, j *job.Job,
	eval *hcl.EvalContext, noLogs bool,
) int {
	outcome, err := d.Run(ctx, origin, j, eval)

	if outcome != nil {
		for i := range outcome.Tasks {
			m.reportTask(&outcome.Tasks[i], noLogs)
		}
	}

	if err != nil {
		return m.reportFailure(j, err)
	}

	if !outcome.Succeeded() {
		return ExitFailure
	}

	return ExitSuccess
}

// reportFailure renders a job that never produced a result.
func (m *Meta) reportFailure(j *job.Job, err error) int {
	switch {
	case errors.Is(err, context.Canceled):
		m.Ui.Error(fmt.Sprintf("Job %q was interrupted. The execution was stopped.", j.Name))

		return ExitNoCapacity

	case errors.Is(err, dispatch.ErrNoCandidates),
		errors.Is(err, dispatch.ErrExhausted):
		m.Ui.Error(fmt.Sprintf("Job %q did not run: %s", j.Name, err))

		return ExitNoCapacity

	default:
		return m.Errorf("Job %q failed: %s", j.Name, err)
	}
}

// reportTask renders one task's result.
func (m *Meta) reportTask(outcome *dispatch.TaskOutcome, noLogs bool) {
	if outcome.Result == nil {
		m.renderRejections(outcome)

		return
	}

	// Only when nothing streamed it already, or a build prints twice.
	if !noLogs && !outcome.Streamed && len(outcome.Result.Logs) > 0 {
		m.Ui.Output(strings.TrimRight(string(outcome.Result.Logs), "\n"))

		if outcome.Result.LogsTruncated {
			m.Ui.Warn("Output was truncated.")
		}
	}

	m.Ui.Error(fmt.Sprintf("==> %s %s on %s in %s",
		outcome.Task, verdict(outcome), outcome.Provider, outcome.Result.Duration))

	if outcome.Rerouted() {
		m.Ui.Error(fmt.Sprintf("    rerouted after %d attempts", outcome.Tried()))
	}

	m.renderRefusals(outcome)
}

// renderRejections explains a task that had nowhere to go.
func (m *Meta) renderRejections(outcome *dispatch.TaskOutcome) {
	for i := range outcome.Rejections {
		r := &outcome.Rejections[i]

		m.Ui.Error(fmt.Sprintf("    %s: %s %s", r.Provider, r.Reason, r.Detail))
	}

	m.renderRefusals(outcome)
}

// renderRefusals explains providers the ledger turned away at dispatch.
//
// Worded as the admission rejection they are: the quota admission saw was
// spent by the time dispatch got there.
func (m *Meta) renderRefusals(outcome *dispatch.TaskOutcome) {
	for i := range outcome.Attempts {
		a := &outcome.Attempts[i]

		if a.Refused {
			m.Ui.Error(fmt.Sprintf("    %s: %s %s", a.Provider, scheduler.ReasonQuotaExhausted, a.Err))
		}
	}
}

// verdict renders whether the task itself passed.
func verdict(outcome *dispatch.TaskOutcome) string {
	if outcome.Succeeded() {
		return "succeeded"
	}

	if code := outcome.Result.ExitCode; code != nil {
		return fmt.Sprintf("failed (exit %d)", *code)
	}

	return "failed"
}

// progress reports a state change to stderr as it happens.
func (m *Meta) progress(e dispatch.Event) {
	line := fmt.Sprintf("==> %s %s on %s", e.Task, e.State, e.Provider)

	if e.Attempt > 1 {
		line += fmt.Sprintf(" (attempt %d)", e.Attempt)
	}

	m.Ui.Error(line)
}
