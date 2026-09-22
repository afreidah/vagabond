// -------------------------------------------------------------------------------
// vagabond job run
//
// Author: Alex Freidah
//
// Dispatches a job and waits for it. The selection is the same one job plan
// prints, because both call the same admission and ranking.
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
	"os/signal"
	"strings"
	"syscall"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/registry"
)

// JobRunCommand implements `vagabond job run`.
type JobRunCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *JobRunCommand) Synopsis() string {
	return "Run a job on the best available provider"
}

// Help returns the full usage text.
func (c *JobRunCommand) Help() string {
	text := `
Usage: vagabond job run [options] <path>

  Admits a job against the configured providers, dispatches it to the best one,
  and waits for it to finish.

  Tasks run in the order they are declared, and the job stops at the first one
  that fails. A provider that fails to give an answer is abandoned and the next
  eligible one is tried, as far as the task's retry block allows. A task that
  ran and exited non-zero is an answer, not an outage, and is never retried
  elsewhere.

  Where the provider supports it, the task's output is streamed as it arrives.
  Output goes to stdout and progress to stderr, so redirecting stdout captures
  the build alone.

  Interrupting the command stops the execution on the provider before exiting.

  Reads from standard input when the path is "-".

  Run will return one of the following exit codes:
    * 0: Every task ran and exited zero.
    * 1: A task ran and failed, or the job could not be read.
    * 2: The work never ran: nothing was eligible, or every provider failed.

Run Options:

  -config <path>
    Configuration file or directory describing the providers to run against.
    A directory loads every .hcl file inside it.

    Defaults to $VAGABOND_CONFIG, then the first of these that exists:
    ./vagabond.hcl, the user configuration directory, /etc/vagabond.d.

  -meta <key>=<value>
    Supply job metadata, repeatable. Values are substituted into the job before
    it is dispatched.

  -no-logs
    Do not print the task's output. The exit status is still reported.
`

	return strings.TrimSpace(text)
}

// Run dispatches the named specification.
func (c *JobRunCommand) Run(args []string) int {
	var (
		meta       metaFlags
		configPath string
		noLogs     bool
	)

	flags := c.FlagSet("job run")
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")
	flags.StringVar(&configPath, "config", "", "provider configuration file or directory")
	flags.BoolVar(&noLogs, "no-logs", false, "do not print the task's output")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	paths := flags.Args()
	if len(paths) != 1 {
		return c.Errorf("This command takes one argument: <path>\n\n%s", c.Help())
	}

	spec, code := c.loadJob(paths[0], meta)
	if spec == nil {
		return code
	}

	// Interrupt stops the execution rather than the process alone, so that a
	// container is not left running and billing after ctrl-C.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg, code := c.loadRegistry(ctx, configPath)
	if reg == nil {
		return code
	}

	return c.run(ctx, spec, meta, reg, noLogs)
}

// -------------------------------------------------------------------------
// DISPATCH
// -------------------------------------------------------------------------

// run dispatches every job in the specification.
func (c *JobRunCommand) run(
	ctx context.Context, spec *job.File, meta metaFlags,
	reg *registry.Registry, noLogs bool,
) int {
	if len(reg.Inputs()) == 0 {
		return c.Errorf("No providers are configured, so there is nothing to run on.")
	}

	opts := []dispatch.Option{dispatch.WithProgress(c.progress)}
	if !noLogs {
		opts = append(opts, dispatch.WithLogs(os.Stdout))
	}

	d := dispatch.New(reg, opts...)
	eval := jobspec.EvalContext(meta)

	worst := ExitSuccess

	for i := range spec.Jobs {
		if code := c.runJob(ctx, d, &spec.Jobs[i], eval, noLogs); code > worst {
			worst = code
		}
	}

	return worst
}

// runJob dispatches one job and reports what happened.
func (c *JobRunCommand) runJob(
	ctx context.Context, d *dispatch.Dispatcher, j *job.Job,
	eval *hcl.EvalContext, noLogs bool,
) int {
	outcome, err := d.Run(ctx, j, eval)

	for i := range outcome.Tasks {
		c.reportTask(&outcome.Tasks[i], noLogs)
	}

	if err != nil {
		return c.reportFailure(j, err)
	}

	if !outcome.Succeeded() {
		return ExitFailure
	}

	return ExitSuccess
}

// reportFailure renders a job that never produced a result.
func (c *JobRunCommand) reportFailure(j *job.Job, err error) int {
	switch {
	case errors.Is(err, context.Canceled):
		c.Ui.Error(fmt.Sprintf("Job %q was interrupted. The execution was stopped.", j.Name))

		return ExitNoCapacity

	case errors.Is(err, dispatch.ErrNoCandidates),
		errors.Is(err, dispatch.ErrExhausted):
		c.Ui.Error(fmt.Sprintf("Job %q did not run: %s", j.Name, err))

		return ExitNoCapacity

	default:
		return c.Errorf("Job %q failed: %s", j.Name, err)
	}
}

// reportTask renders one task's result.
func (c *JobRunCommand) reportTask(outcome *dispatch.TaskOutcome, noLogs bool) {
	if outcome.Result == nil {
		c.renderRejections(outcome)

		return
	}

	// Only when nothing streamed it already, or a build prints twice.
	if !noLogs && !outcome.Streamed && len(outcome.Result.Logs) > 0 {
		c.Ui.Output(strings.TrimRight(string(outcome.Result.Logs), "\n"))

		if outcome.Result.LogsTruncated {
			c.Ui.Warn("Output was truncated.")
		}
	}

	c.Ui.Error(fmt.Sprintf("==> %s %s on %s in %s",
		outcome.Task, verdict(outcome), outcome.Provider, outcome.Result.Duration))

	if outcome.Rerouted() {
		c.Ui.Error(fmt.Sprintf("    rerouted after %d attempts", len(outcome.Attempts)))
	}
}

// renderRejections explains a task that had nowhere to go.
func (c *JobRunCommand) renderRejections(outcome *dispatch.TaskOutcome) {
	for i := range outcome.Rejections {
		r := &outcome.Rejections[i]

		c.Ui.Error(fmt.Sprintf("    %s: %s %s", r.Provider, r.Reason, r.Detail))
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
func (c *JobRunCommand) progress(e dispatch.Event) {
	line := fmt.Sprintf("==> %s %s on %s", e.Task, e.State, e.Provider)

	if e.Attempt > 1 {
		line += fmt.Sprintf(" (attempt %d)", e.Attempt)
	}

	c.Ui.Error(line)
}
