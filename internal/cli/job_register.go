// -------------------------------------------------------------------------------
// vagabond job register
//
// Author: Alex Freidah
//
// Stores a job on the server so it can be dispatched by name. Nothing runs. A
// new version is made only when the job changed.
//
// Validated in full before sending, with each declared ${meta.key} standing as
// its own text: the values arrive at dispatch, and a reference to an undeclared
// key is caught here rather than then.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"strings"
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
  Nothing runs.

  A new version is made only when the job changed. Registering a stopped job
  makes a new version and makes it dispatchable again.

  The job is validated in full. Its declared metadata stands as written, since
  the values are supplied at dispatch.

  Reads from standard input when the path is "-".

Register Options:

  -address <addr>
    The server to register with. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.

  -namespace <name>
    The namespace to register in, for a job that names none. Defaults to
    $VAGABOND_NAMESPACE, then "default".
`

	return strings.TrimSpace(text)
}

// Run validates the job file and registers it, reporting whether it made a new
// version.
func (c *JobRegisterCommand) Run(args []string) int {
	flags := c.FlagSet("job register")
	c.clientFlags(flags)

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	paths := flags.Args()
	if len(paths) != 1 {
		return c.Errorf("This command takes one argument: <path>\n\n%s", c.Help())
	}

	src, code := c.checkRegister(paths[0])
	if src == nil {
		return code
	}

	client, err := c.client()
	if err != nil {
		return c.Errorf("%s", err)
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

	return ExitSuccess
}
