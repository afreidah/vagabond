// -------------------------------------------------------------------------------
// vagabond job stop
//
// Author: Alex Freidah
//
// Deregisters a job, as nomad job stop does: it can no longer be dispatched,
// its versions and executions are kept, and registering it again revives it.
// Purging arrives with garbage collection.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
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

  Executions already running are not affected. Requires a store block.

Stop Options:

  -config <path>
    Configuration file or directory. Defaults as for job run.

  -namespace <name>
    The namespace the job is registered in. Defaults to $VAGABOND_NAMESPACE,
    then "default".
`

	return strings.TrimSpace(text)
}

// Run stops the named job.
func (c *JobStopCommand) Run(args []string) int {
	var configPath, namespace string

	flags := c.FlagSet("job stop")
	flags.StringVar(&configPath, "config", "", "provider configuration file or directory")
	flags.StringVar(&namespace, "namespace", os.Getenv(namespaceEnv), "namespace the job is registered in")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	names := flags.Args()
	if len(names) != 1 {
		return c.Errorf("This command takes one argument: <name>\n\n%s", c.Help())
	}

	ctx := context.Background()

	reg, s, finish, code := c.loadJobStores(ctx, configPath, "job stop")
	if reg == nil {
		return code
	}

	defer finish()

	ns, err := namespaceOf(namespace, reg)
	if err != nil {
		return c.Errorf("%s", err)
	}

	if err := s.jobs.Stop(ctx, ns, names[0], time.Now()); err != nil {
		return c.Errorf("%s", err)
	}

	c.Ui.Output(fmt.Sprintf("Job %q stopped in namespace %q.", names[0], ns))

	return ExitSuccess
}
