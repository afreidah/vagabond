// -------------------------------------------------------------------------------
// vagabond execution logs
//
// Author: Alex Freidah
//
// Prints an execution's stored output, the last 64 KiB, which is where a build
// fails; or with -f follows it live until the execution ends, as job run does
// for its own tasks.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"
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

  Prints the output stored for an execution, once it has a result. The last
  64 KiB is kept. With -f, streams the output while the execution runs and
  exits when it ends.

Logs Options:

  -f
    Follow the output while the execution runs. An interrupt stops following
    and leaves the execution running.

  -address <addr>
    The server to read. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.
`

	return strings.TrimSpace(text)
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

	logs, err := client.Logs(context.Background(), ids[0], 0)
	if err != nil {
		return c.apiFailure(err)
	}

	if len(logs) > 0 {
		c.Ui.Output(strings.TrimRight(string(logs), "\n"))
	}

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
