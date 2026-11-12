// -------------------------------------------------------------------------------
// vagabond execution logs
//
// Author: Alex Freidah
//
// Prints an execution's stored output, or with -f follows it live until the
// execution ends, as job run does for its own tasks. Either way the warnings
// are a run's: output dropped past the server's ceiling, and output the
// provider did not give.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

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

  Prints the output stored for an execution, once it has ended. The server
  keeps up to max_log_mib of it (16 MiB by default), the end of it, for
  log_retention after the execution ends (7 days by default). With -f,
  streams the output while the execution runs, on providers that can stream
  it (Cloud Run, Code Engine and agent pools), prints what did not stream once
  it ends, and exits.

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
		return c.follow(client, ids[0])
	}

	ctx := context.Background()

	e, err := client.Execution(ctx, ids[0])
	if err != nil {
		return c.apiFailure(err)
	}

	c.newFollower(client, false).rest(ctx, e)

	return ExitSuccess
}

// follow streams id's output until the execution ends, then prints whatever
// of its stored copy did not stream. An interrupt stops following only.
func (c *ExecutionLogsCommand) follow(client *api.Client, id string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	f := c.newFollower(client, false)

	for {
		e, err := client.Execution(ctx, id)

		switch {
		case errors.Is(err, context.Canceled):
			return ExitSuccess
		case err != nil:
			return c.apiFailure(err)
		}

		if e.HasResult || e.Ended != nil {
			f.rest(ctx, e)

			return ExitSuccess
		}

		f.watch(ctx, e)

		select {
		case <-ctx.Done():
			return ExitSuccess
		case <-time.After(pollInterval):
		}
	}
}
