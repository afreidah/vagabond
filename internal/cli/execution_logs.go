// -------------------------------------------------------------------------------
// vagabond execution logs
//
// Author: Alex Freidah
//
// Prints an execution's stored output: the last 64 KiB, which is where a
// build fails.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"strings"
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
  64 KiB is kept.

Logs Options:

  -address <addr>
    The server to read. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.
`

	return strings.TrimSpace(text)
}

// Run reads the execution's stored output and prints it to stdout.
func (c *ExecutionLogsCommand) Run(args []string) int {
	flags := c.FlagSet("execution logs")
	c.clientFlags(flags)

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	ids := flags.Args()
	if len(ids) != 1 {
		return c.Errorf("This command takes one argument: <id>\n\n%s", c.Help())
	}

	client, err := c.client()
	if err != nil {
		return c.Errorf("%s", err)
	}

	logs, err := client.Logs(context.Background(), ids[0])
	if err != nil {
		return c.apiFailure(err)
	}

	if len(logs) > 0 {
		c.Ui.Output(strings.TrimRight(string(logs), "\n"))
	}

	return ExitSuccess
}
