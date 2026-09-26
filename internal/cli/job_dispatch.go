// -------------------------------------------------------------------------------
// vagabond job dispatch
//
// Author: Alex Freidah
//
// Runs a registered job's current version by name on the server, and waits for
// it, reporting as job run does. A job that is not parameterized takes no
// metadata, and a parameterized one refuses keys it did not declare and
// requires the ones it marked required.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// JobDispatchCommand implements `vagabond job dispatch`.
type JobDispatchCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *JobDispatchCommand) Synopsis() string {
	return "Run a registered job by name"
}

// Help returns the full usage text.
func (c *JobDispatchCommand) Help() string {
	text := `
Usage: vagabond job dispatch [options] <name>

  Runs the current version of a registered job and waits for it, exactly as
  job run does. Every execution records the job version and a dispatch ID
  shared by the run's tasks.

  A job without a parameterized block takes no metadata. A parameterized job
  refuses keys in neither meta_required nor meta_optional, and requires every
  key in meta_required. Each task receives VAGABOND_META_<KEY>.

  Exit codes are those of job run.

Dispatch Options:

  -address <addr>
    The server to dispatch on. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.

  -meta <key>=<value>
    Supply job metadata, repeatable.

  -namespace <name>
    The namespace the job is registered in. Defaults to $VAGABOND_NAMESPACE,
    then "default".

  -no-logs
    Do not print the task's output. The exit status is still reported.
`

	return strings.TrimSpace(text)
}

// Run dispatches the named job's current version and follows the run to its
// end.
func (c *JobDispatchCommand) Run(args []string) int {
	var (
		meta   metaFlags
		noLogs bool
	)

	flags := c.FlagSet("job dispatch")
	c.clientFlags(flags)
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")
	flags.BoolVar(&noLogs, "no-logs", false, "do not print the task's output")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	names := flags.Args()
	if len(names) != 1 {
		return c.Errorf("This command takes one argument: <name>\n\n%s", c.Help())
	}

	client, err := c.client()
	if err != nil {
		return c.Errorf("%s", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started, err := client.Dispatch(ctx, c.namespace, names[0], meta)
	if err != nil {
		return c.apiFailure(err)
	}

	return c.follow(ctx, client, started, noLogs)
}
