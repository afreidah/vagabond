// -------------------------------------------------------------------------------
// vagabond execution logs
//
// Author: Alex Freidah
//
// Prints the output the server has stored for an execution, or with -f
// follows it as it is stored until the execution ends, as job run does for its
// own tasks. Either way the warnings are a run's: output dropped past the
// server's ceiling, and output the provider did not give.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/afreidah/vagabond/internal/api"
)

// ExecutionLogsCommand implements `vagabond execution logs`.
type ExecutionLogsCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *ExecutionLogsCommand) Synopsis() string {
	return "Print an execution's output"
}

// Help returns the full usage text.
func (c *ExecutionLogsCommand) Help() string {
	text := `
Usage: vagabond execution logs [options] <id>

  Prints the output the server has stored for an execution so far, which it
  stores while the execution runs. The server keeps up to max_log_mib of it
  (16 MiB by default), the end of it, for log_retention after the execution
  ends (7 days by default). With -f, prints the output as it is stored until
  the execution ends, then exits.

Logs Options:

  -f
    Follow the output while the execution runs. An interrupt stops following
    and leaves the execution running.
`

	return clientHelp(text)
}

// Run prints the execution's stored output to stdout, or follows it with -f.
func (c *ExecutionLogsCommand) Run(args []string) int {
	flags := c.FlagSet("execution logs")

	var follow bool

	flags.BoolVar(&follow, "f", false, "follow the output while the execution runs")

	client, ids := c.connect(flags, args, oneArg("<id>"), c.Help())
	if client == nil {
		return ExitFailure
	}

	if follow {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		return c.follow(ctx, client, ids[0])
	}

	ctx := context.Background()

	e, err := client.Execution(ctx, ids[0])
	if err != nil {
		return c.apiFailure(err)
	}

	f := c.newFollower(client, false)
	out := f.output()

	f.closeOutput(e, out, client.Logs(ctx, e.ID, 0, out))

	return ExitSuccess
}

// follow prints id's output as it is stored until the execution ends. An
// interrupt, ctx ending, stops following only, as does a server that stays
// unreachable past the retries.
func (c *ExecutionLogsCommand) follow(ctx context.Context, client *api.Client, id string) int {
	f := c.newFollower(client, false)
	reads := c.retrying(client)

	var last *api.Execution

	err := poll(ctx, func(ctx context.Context) (*api.Execution, error) {
		return reads.Execution(ctx, id)
	}, func(e *api.Execution) bool {
		last = e

		if e.HasResult || e.Ended != nil {
			return true
		}

		f.watch(ctx, e)

		return false
	})

	switch {
	case errors.Is(err, context.Canceled):
		return ExitSuccess
	case err != nil:
		return c.lost(err, "execution", id)
	}

	f.rest(ctx, last)

	return ExitSuccess
}
