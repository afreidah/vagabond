// -------------------------------------------------------------------------------
// vagabond execution list
//
// Author: Alex Freidah
//
// A namespace's most recent executions across every job, newest first,
// narrowed by job, state and provider: how a run is found without its ID.
// -------------------------------------------------------------------------------

package cli

import (
	"context"

	"github.com/afreidah/vagabond/internal/api"
)

// ExecutionListCommand implements `vagabond execution list`.
type ExecutionListCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *ExecutionListCommand) Synopsis() string {
	return "List recent executions"
}

// Help returns the full usage text.
func (c *ExecutionListCommand) Help() string {
	text := `
Usage: vagabond execution list [options]

  Lists the namespace's most recent executions, newest first, across every
  job, registered or run from a file. Each ID reads with execution status and
  execution logs.

List Options:

  -namespace <name>
    Defaults to $VAGABOND_NAMESPACE, then "default".

  -job <name>
    Only the job's executions.

  -state <state>
    Only executions in the state: pending, submitted, accepted, running,
    succeeded, failed, cancelled or lost.

  -provider <name>
    Only executions on the provider.

  -limit <n>
    How many to list, at least 1. Defaults to 20; the server lists at most
    1000.
`

	return clientHelp(text)
}

// Run lists the executions the flags pick, or says none match.
func (c *ExecutionListCommand) Run(args []string) int {
	flags := c.FlagSet("execution list")
	c.namespaceFlag(flags)

	var f api.ExecutionFilter

	flags.StringVar(&f.Job, "job", "", "only the job's executions")
	flags.StringVar(&f.State, "state", "", "only executions in the state")
	flags.StringVar(&f.Provider, "provider", "", "only executions on the provider")
	flags.IntVar(&f.Limit, "limit", 0, "how many to list")

	client, _ := c.connect(flags, args, noArgs, c.Help())
	if client == nil {
		return ExitFailure
	}

	if flagSet(flags, "limit") && f.Limit < 1 {
		return c.Errorf("Error: -limit %d lists nothing; give at least 1.", f.Limit)
	}

	list, err := client.ListExecutions(context.Background(), c.namespace, f)
	if err != nil {
		return c.apiFailure(err)
	}

	if len(list) == 0 {
		c.Ui.Output("No executions match.")

		return ExitSuccess
	}

	c.Ui.Output(executionTable(list, true))

	return ExitSuccess
}
