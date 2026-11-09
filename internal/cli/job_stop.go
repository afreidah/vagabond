// -------------------------------------------------------------------------------
// vagabond job stop
//
// Author: Alex Freidah
//
// Deregisters a job so it can no longer be dispatched. Its versions and
// executions are kept, and registering it again revives it.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"strings"
)

// JobStopCommand implements `vagabond job stop`.
type JobStopCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *JobStopCommand) Synopsis() string {
	return "Deregister a job so it can no longer be dispatched"
}

// Help returns the full usage text.
func (c *JobStopCommand) Help() string {
	text := `
Usage: vagabond job stop [options] <name>

  Deregisters a registered job. It can no longer be dispatched; its versions and
  executions are kept, and registering it again makes it dispatchable.

  Executions already running are not affected.

Stop Options:

  -address <addr>
    The server the job is registered with. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.

  -namespace <name>
    The namespace the job is registered in. Defaults to $VAGABOND_NAMESPACE,
    then "default".
`

	return strings.TrimSpace(text)
}

// Run stops the named job. Its executions already running are left alone.
func (c *JobStopCommand) Run(args []string) int {
	client, names := c.connect(c.FlagSet("job stop"), args, oneArg("<name>"), c.Help())
	if client == nil {
		return ExitFailure
	}

	stopped, err := client.StopJob(context.Background(), c.namespace, names[0])
	if err != nil {
		return c.apiFailure(err)
	}

	c.Ui.Output(fmt.Sprintf("Job %q stopped in namespace %q.", stopped.Name, stopped.Namespace))

	return ExitSuccess
}
