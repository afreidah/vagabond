// -------------------------------------------------------------------------------
// vagabond job run
//
// Author: Alex Freidah
//
// Runs a job file on the server and waits for it. The selection is the same one
// job plan prints, because the server calls the same admission and ranking for
// both. Nothing is registered; job register and job dispatch are the path for a
// job that runs again by name.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

  Sends a job to the server, which dispatches it to the best provider, and
  waits for it to finish. The job is not registered.

  Tasks run in the order they are declared, and the job stops at the first one
  that fails. A provider that fails to give an answer is abandoned and the next
  eligible one is tried, as far as the task's retry block allows. A task that
  ran and exited non-zero is an answer, not an outage, and is never retried
  elsewhere.

  Each task's output is printed when it finishes. Output goes to stdout and
  progress to stderr, so redirecting stdout captures the build alone.

  Interrupting the command stops the execution on the provider before exiting.

  Reads from standard input when the path is "-".

  Run will return one of the following exit codes:
    * 0: Every task ran and exited zero.
    * 1: A task ran and failed, or the job could not be sent.
    * 2: The work never ran: nothing was eligible, or every provider failed.

Run Options:

  -address <addr>
    The server to run on. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.

  -meta <key>=<value>
    Supply job metadata, repeatable. Values are substituted into the job before
    it is dispatched, and each task receives VAGABOND_META_<KEY>.

  -namespace <name>
    The namespace to run in, for a job that names none. Defaults to
    $VAGABOND_NAMESPACE, then "default". A job naming a different one is an
    error.

  -no-logs
    Do not print the task's output. The exit status is still reported.
`

	return strings.TrimSpace(text)
}

// Run validates the job file, sends it, and follows the run to its end.
func (c *JobRunCommand) Run(args []string) int {
	var (
		meta   metaFlags
		noLogs bool
	)

	flags := c.FlagSet("job run")
	c.clientFlags(flags)
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")
	flags.BoolVar(&noLogs, "no-logs", false, "do not print the task's output")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	paths := flags.Args()
	if len(paths) != 1 {
		return c.Errorf("This command takes one argument: <path>\n\n%s", c.Help())
	}

	src, code := c.checkJob(paths[0], meta)
	if src == nil {
		return code
	}

	client, err := c.client()
	if err != nil {
		return c.Errorf("%s", err)
	}

	// Interrupt stops the execution rather than the command alone, so that a
	// container is not left running and billing after ctrl-C.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started, err := client.Run(ctx, c.namespace, string(src), meta)
	if err != nil {
		return c.apiFailure(err)
	}

	return c.follow(ctx, client, started, noLogs)
}
