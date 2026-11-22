// -------------------------------------------------------------------------------
// vagabond job register
//
// Author: Alex Freidah
//
// Stores a job on the server so it can be dispatched by name. Nothing runs
// now; a periodic job launches on its schedule, and its next launch is
// printed. A new version is made only when the job changed.
//
// Validated in full before sending, with each declared ${meta.key} standing as
// its own text: the values arrive at dispatch, and a reference to an undeclared
// key is caught here rather than then.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"time"
)

// JobRegisterCommand implements `vagabond job register`.
type JobRegisterCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *JobRegisterCommand) Synopsis() string {
	return "Store a job so it can be dispatched by name"
}

// Help returns the full usage text.
func (c *JobRegisterCommand) Help() string {
	text := `
Usage: vagabond job register [options] <path>

  Stores the job in the file on the server so it can be dispatched by name.
  Nothing runs now.

  A job with a periodic block launches on its schedule from then on, and the
  time of its next launch is printed. Its first launch is the first time a
  cron matches after it is registered, never one that has already passed.

  A new version is made only when the job changed. Registering a stopped job
  makes a new version and makes it dispatchable again.

  The job is validated in full. Its declared metadata stands as written, since
  the values are supplied at dispatch.

  Reads from standard input when the path is "-".

Register Options:

  -namespace <name>
    The namespace to register in, for a job that names none. Defaults to
    $VAGABOND_NAMESPACE, then "default".
`

	return clientHelp(text)
}

// Run validates the job file and registers it, reporting whether it made a new
// version.
func (c *JobRegisterCommand) Run(args []string) int {
	flags := c.FlagSet("job register")
	c.namespaceFlag(flags)

	client, paths := c.connect(flags, args, oneArg("<path>"), c.Help())
	if client == nil {
		return ExitFailure
	}

	src, code := c.checkRegister(paths[0])
	if src == nil {
		return code
	}

	registered, err := client.Register(context.Background(), c.namespace, string(src))
	if err != nil {
		return c.apiFailure(err)
	}

	if registered.Changed {
		c.Ui.Output(fmt.Sprintf("Job %q registered as version %d in namespace %q.",
			registered.Name, registered.Version, registered.Namespace))
	} else {
		c.Ui.Output(fmt.Sprintf("Job %q is unchanged at version %d in namespace %q.",
			registered.Name, registered.Version, registered.Namespace))
	}

	if registered.NextLaunch != nil {
		c.Ui.Output(fmt.Sprintf("Next launch: %s.", when(*registered.NextLaunch, time.Now())))
	}

	return ExitSuccess
}
