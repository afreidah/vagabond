// -------------------------------------------------------------------------------
// vagabond job dispatch
//
// Author: Alex Freidah
//
// Runs a registered job's current version by name, and waits for it, reporting
// as job run does. Metadata is checked as Nomad checks it: a job that is not
// parameterized takes none, and a parameterized one refuses keys it did not
// declare and requires the ones it marked required.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/jobspec"
)

// JobDispatchCommand implements `vagabond job dispatch`.
type JobDispatchCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *JobDispatchCommand) Synopsis() string {
	return "Run a registered job by name"
}

// Help returns the full usage text.
func (c *JobDispatchCommand) Help() string {
	text := `
Usage: vagabond job dispatch [options] <name>

  Runs the current version of a registered job and waits for it, exactly as
  job run does. Every execution records the job version and a dispatch ID
  shared by the run's tasks.

  A job without a parameterized block takes no metadata. A parameterized job
  refuses keys in neither meta_required nor meta_optional, and requires every
  key in meta_required. Each task receives VAGABOND_META_<KEY>.

  Requires a store block. Exit codes are those of job run.

Dispatch Options:

  -config <path>
    Configuration file or directory. Defaults as for job run.

  -meta <key>=<value>
    Supply job metadata, repeatable.

  -namespace <name>
    The namespace the job is registered in. Defaults to $VAGABOND_NAMESPACE,
    then "default".

  -no-logs
    Do not print the task's output. The exit status is still reported.
`

	return strings.TrimSpace(text)
}

// Run dispatches the named job.
func (c *JobDispatchCommand) Run(args []string) int {
	var (
		meta       metaFlags
		configPath string
		namespace  string
		noLogs     bool
	)

	flags := c.FlagSet("job dispatch")
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")
	flags.StringVar(&configPath, "config", "", "provider configuration file or directory")
	flags.StringVar(&namespace, "namespace", os.Getenv(namespaceEnv), "namespace the job is registered in")
	flags.BoolVar(&noLogs, "no-logs", false, "do not print the task's output")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	names := flags.Args()
	if len(names) != 1 {
		return c.Errorf("This command takes one argument: <name>\n\n%s", c.Help())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg, s, finish, code := c.loadJobStores(ctx, configPath, "job dispatch")
	if reg == nil {
		return code
	}

	defer finish()

	ns, err := namespaceOf(namespace, reg)
	if err != nil {
		return c.Errorf("%s", err)
	}

	spec, version, code := c.loadRegistered(ctx, s, ns, names[0], meta)
	if spec == nil {
		return code
	}

	// Minted here rather than by Run, so it can be printed before anything
	// runs and the dispatch found again in job status.
	id, err := execution.NewID()
	if err != nil {
		return c.Errorf("Generating a dispatch ID: %s", err)
	}

	c.Ui.Error(fmt.Sprintf("==> dispatch %s of %q version %d", id, version.Name, version.Version))

	d := c.newDispatcher(ctx, reg, s, noLogs)
	origin := dispatch.Origin{Namespace: ns, JobVersion: version.Version, Dispatch: id}

	return c.runJob(ctx, d, origin, &spec.Jobs[0], jobspec.EvalContext(meta), noLogs)
}
