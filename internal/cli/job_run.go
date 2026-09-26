// -------------------------------------------------------------------------------
// vagabond job run
//
// Author: Alex Freidah
//
// Dispatches a job from a file and waits for it. The selection is the same one
// job plan prints, because both call the same admission and ranking. Nothing is
// registered; job register and job dispatch are the path for a job that runs
// again by name.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/registry"
)

// JobRunCommand implements `vagabond job run`.
type JobRunCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *JobRunCommand) Synopsis() string {
	return "Run a job on the best available provider"
}

// Help returns the full usage text.
func (c *JobRunCommand) Help() string {
	text := `
Usage: vagabond job run [options] <path>

  Admits a job against the configured providers, dispatches it to the best one,
  and waits for it to finish. The job is not registered.

  Tasks run in the order they are declared, and the job stops at the first one
  that fails. A provider that fails to give an answer is abandoned and the next
  eligible one is tried, as far as the task's retry block allows. A task that
  ran and exited non-zero is an answer, not an outage, and is never retried
  elsewhere.

  Where the provider supports it, the task's output is streamed as it arrives.
  Output goes to stdout and progress to stderr, so redirecting stdout captures
  the build alone.

  Interrupting the command stops the execution on the provider before exiting.

  Reads from standard input when the path is "-".

  Run will return one of the following exit codes:
    * 0: Every task ran and exited zero.
    * 1: A task ran and failed, or the job could not be read.
    * 2: The work never ran: nothing was eligible, or every provider failed.

Run Options:

  -config <path>
    Configuration file or directory describing the providers to run against.
    A directory loads every .hcl file inside it.

    Defaults to $VAGABOND_CONFIG, then the first of these that exists:
    ./vagabond.hcl, the user configuration directory, /etc/vagabond.d.

  -meta <key>=<value>
    Supply job metadata, repeatable. Values are substituted into the job before
    it is dispatched, and each task receives VAGABOND_META_<KEY>.

  -namespace <name>
    The namespace to run in, for a job that names none. Defaults to
    $VAGABOND_NAMESPACE, then "default". A job naming a different one is an
    error.

  -no-logs
    Do not print the task's output. The exit status is still reported.

  -untracked
    Dispatch even when the configured usage store cannot be reached. Usage is
    then kept in memory only, so this run is not charged against the quota
    other runs see. A store that can be reached is always used.
`

	return strings.TrimSpace(text)
}

// Run dispatches the named specification.
func (c *JobRunCommand) Run(args []string) int {
	var (
		meta       metaFlags
		configPath string
		namespace  string
		noLogs     bool
		untracked  bool
	)

	flags := c.FlagSet("job run")
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")
	flags.StringVar(&namespace, "namespace", os.Getenv(namespaceEnv), "namespace for a job that names none")
	flags.StringVar(&configPath, "config", "", "provider configuration file or directory")
	flags.BoolVar(&noLogs, "no-logs", false, "do not print the task's output")
	flags.BoolVar(&untracked, "untracked", false, "dispatch even when the usage store is unreachable")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	paths := flags.Args()
	if len(paths) != 1 {
		return c.Errorf("This command takes one argument: <path>\n\n%s", c.Help())
	}

	spec, code := c.loadJob(paths[0], meta)
	if spec == nil {
		return code
	}

	// Interrupt stops the execution rather than the process alone, so that a
	// container is not left running and billing after ctrl-C.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg, store, code := c.loadRegistry(ctx, configPath)
	if reg == nil {
		return code
	}

	if store == nil {
		c.Ui.Warn("No store is configured, so this run's usage is not recorded.")
	}

	s, finish, code := c.loadStores(ctx, store, reg, untracked, "run")
	if s == nil {
		return code
	}

	defer finish()

	return c.run(ctx, spec, meta, namespace, reg, s, noLogs)
}

// run dispatches every job in the specification.
func (c *JobRunCommand) run(
	ctx context.Context, spec *job.File, meta metaFlags, namespaceFlag string,
	reg *registry.Registry, s *stores, noLogs bool,
) int {
	if len(reg.Names()) == 0 {
		return c.Errorf("No providers are configured, so there is nothing to run on.")
	}

	namespaces, err := resolveNamespaces(namespaceFlag, spec, reg)
	if err != nil {
		return c.Errorf("%s", err)
	}

	d := c.newDispatcher(ctx, reg, s, noLogs)
	eval := jobspec.EvalContext(meta)

	worst := ExitSuccess

	for i := range spec.Jobs {
		origin := dispatch.Origin{Namespace: namespaces[i]}

		if code := c.runJob(ctx, d, origin, &spec.Jobs[i], eval, noLogs); code > worst {
			worst = code
		}
	}

	return worst
}
