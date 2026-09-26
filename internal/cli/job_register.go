// -------------------------------------------------------------------------------
// vagabond job register
//
// Author: Alex Freidah
//
// Stores a job so it can be dispatched by name. Nothing runs. A new version is
// made only when the job changed, as Nomad does.
//
// Validated in full, with each declared ${meta.key} standing as its own text:
// the values arrive at dispatch, and a reference to an undeclared key is
// caught here rather than then.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/jobspec"
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

  Stores the job in the file so it can be dispatched by name. Nothing runs.

  A new version is made only when the job changed. Registering a stopped job
  makes a new version and makes it dispatchable again.

  The job is validated in full. Its declared metadata stands as written, since
  the values are supplied at dispatch.

  Requires a store block. Reads from standard input when the path is "-".

Register Options:

  -config <path>
    Configuration file or directory. Defaults as for job run.

  -namespace <name>
    The namespace to register in, for a job that names none. Defaults to
    $VAGABOND_NAMESPACE, then "default".
`

	return strings.TrimSpace(text)
}

// Run registers the named specification.
func (c *JobRegisterCommand) Run(args []string) int {
	var configPath, namespace string

	flags := c.FlagSet("job register")
	flags.StringVar(&configPath, "config", "", "provider configuration file or directory")
	flags.StringVar(&namespace, "namespace", os.Getenv(namespaceEnv), "namespace for a job that names none")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	paths := flags.Args()
	if len(paths) != 1 {
		return c.Errorf("This command takes one argument: <path>\n\n%s", c.Help())
	}

	src, err := c.readSource(paths[0])
	if err != nil {
		return c.Errorf("%s", err)
	}

	decl, diags := jobspec.Declared(paths[0], src)
	if diags.HasErrors() {
		renderDiagnostics(c.Ui, nil, diags, c.color())

		return ExitFailure
	}

	spec, code := c.loadSource(paths[0], src, decl.References())
	if spec == nil {
		return code
	}

	ctx := context.Background()

	reg, s, finish, code := c.loadJobStores(ctx, configPath, "job register")
	if reg == nil {
		return code
	}

	defer finish()

	j := &spec.Jobs[0]

	ns, err := resolveNamespace(namespace, j, reg)
	if err != nil {
		return c.Errorf("%s", err)
	}

	version, changed, err := s.jobs.Register(ctx, ns, j.Name, src, time.Now())
	if err != nil {
		return c.Errorf("Registering job %q: %s", j.Name, err)
	}

	if changed {
		c.Ui.Output(fmt.Sprintf("Job %q registered as version %d in namespace %q.", j.Name, version, ns))
	} else {
		c.Ui.Output(fmt.Sprintf("Job %q is unchanged at version %d in namespace %q.", j.Name, version, ns))
	}

	return ExitSuccess
}
