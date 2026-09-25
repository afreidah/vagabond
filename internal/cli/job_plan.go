// -------------------------------------------------------------------------------
// vagabond job plan
//
// Author: Alex Freidah
//
// Answers the question validation cannot: given the providers configured right
// now, where would this job run, and why not everywhere else.
//
// It is the first command that needs to know about the world rather than about
// a file, which is why it is also the first that needs configuration. A plan
// against fake providers nobody configured would be a demonstration wearing a
// plan's output, so a missing configuration is an error here rather than a
// fallback: terraform plan does not invent providers either.
//
// Nothing here contacts anything. Admission and ranking are pure functions over
// snapshots gathered beforehand, so a plan is exactly as current as its data
// and says so.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// JobPlanCommand implements `vagabond job plan`.
type JobPlanCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *JobPlanCommand) Synopsis() string {
	return "Show where a job would run, and why not elsewhere"
}

// Help returns the full usage text.
func (c *JobPlanCommand) Help() string {
	text := `
Usage: vagabond job plan [options] <path>

  Admits a job against the configured providers and shows the result: which
  could run it, how each scored, and for every one that could not, the reason.

  Nothing is dispatched and nothing is reserved. Planning the same job twice
  changes nothing and costs nothing.

  Reads from standard input when the path is "-".

  No provider is contacted. Admission reads capability snapshots gathered
  earlier, so a plan is only as current as its data and can be wrong by the
  age of it. Each provider's observation time is shown. Quota is priced from
  the usage store, when one is configured.

  Plan will return one of the following exit codes:
    * 0: At least one provider can run every task.
    * 1: Some task has nowhere to run, or the plan could not be produced.

Plan Options:

  -config <path>
    Configuration file or directory describing the providers to plan against.
    A directory loads every .hcl file inside it.

    Defaults to $VAGABOND_CONFIG, then the first of these that exists:
    ./vagabond.hcl, the user configuration directory, /etc/vagabond.d.

  -meta <key>=<value>
    Supply job metadata, repeatable. Values are substituted into the job before
    it is planned, so a specification that interpolates meta.version is planned
    as it would actually be submitted.

  -namespace <name>
    The namespace to plan in, for a job that names none. Defaults to
    $VAGABOND_NAMESPACE, then "default". A job naming a different one is an
    error.

  -untracked
    Plan even when the configured usage store cannot be reached, pricing every
    provider as if nothing had been spent. A store that can be reached is
    always used.

  -verbose
    Show every scorer's contribution rather than the final score alone.
`

	return strings.TrimSpace(text)
}

// Run plans the named specification against the configured providers.
func (c *JobPlanCommand) Run(args []string) int {
	var (
		meta       metaFlags
		configPath string
		namespace  string
		verbose    bool
		untracked  bool
	)

	flags := c.FlagSet("job plan")
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")
	flags.StringVar(&namespace, "namespace", os.Getenv(namespaceEnv), "namespace for a job that names none")
	flags.StringVar(&configPath, "config", "", "provider configuration file or directory")
	flags.BoolVar(&verbose, "verbose", false, "show each scorer's contribution")
	flags.BoolVar(&untracked, "untracked", false, "plan even when the usage store is unreachable")

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

	ctx := context.Background()

	reg, store, code := c.loadRegistry(ctx, configPath)
	if reg == nil {
		return code
	}

	led, finish, code := c.loadLedger(ctx, store, reg, untracked, "plan")
	if led == nil {
		return code
	}

	defer finish()

	return c.plan(spec, meta, namespace, reg, led, verbose)
}

// -------------------------------------------------------------------------
// PLANNING
// -------------------------------------------------------------------------

// plan admits and ranks every task in the specification.
//
// Reads usage from the ledger and never reserves, so a plan is priced the way a
// run would be and changes nothing.
func (c *JobPlanCommand) plan(
	spec *job.File, meta metaFlags, namespaceFlag string, reg *registry.Registry,
	led dispatch.Ledger, verbose bool,
) int {
	if len(reg.Names()) == 0 {
		return c.Errorf("No providers are configured, so there is nothing to plan against.")
	}

	namespaces, err := resolveNamespaces(namespaceFlag, spec, reg)
	if err != nil {
		return c.Errorf("%s", err)
	}

	ctx := jobspec.EvalContext(meta)

	admitted := true
	first := true

	for i := range spec.Jobs {
		j := &spec.Jobs[i]
		inputs := reg.Inputs(namespaces[i], led.PoolUsage)

		for k := range j.Tasks {
			req, diags := scheduler.NewRequest(&j.Tasks[k], j.Routing, ctx)
			if diags.HasErrors() {
				renderDiagnostics(c.Ui, nil, diags, c.color())

				return ExitFailure
			}

			// Blank line between tasks but not above the first, so a job with
			// one task does not open on an empty line.
			if !first {
				c.Ui.Output("")
			}

			first = false

			if !c.planTask(j, &j.Tasks[k], req, inputs, verbose) {
				admitted = false
			}
		}
	}

	if !admitted {
		return ExitFailure
	}

	return ExitSuccess
}

// planTask renders one task's plan and reports whether anything can run it.
func (c *JobPlanCommand) planTask(
	j *job.Job, task *job.Task, req *scheduler.Request,
	inputs []scheduler.Input, verbose bool,
) bool {
	result := scheduler.Admit(req, inputs)
	ranking := scheduler.Rank(req, result.Candidates)

	c.Ui.Output(fmt.Sprintf("%s.%s (%s)", j.Name, task.Name, task.Driver))
	c.Ui.Output(c.table(ranking, result.Rejections, verbose))

	selected, ok := ranking.Selected()
	if !ok {
		c.Ui.Output("No provider can run this task.")

		if result.Retryable() {
			c.Ui.Output("At least one rejection is transient, so this may be admitted later.")
		}

		return false
	}

	c.Ui.Output(fmt.Sprintf("Selected: %s", selected.Provider))
	c.Ui.Output(fmt.Sprintf("Estimated cost: %s", cost(selected.EstimatedCost)))

	return true
}

// cost renders what an execution would be charged.
//
// Free is spelled rather than printed as a zero, because "Estimated cost: 0"
// asks the reader to know what the units are and this project's entire answer
// is that there are none to pay. A non-zero cost prints as a number, since
// job.Cost deliberately does not pin its unit yet and inventing a currency
// symbol here would pin it by accident.
func cost(c job.Cost) string {
	if c.Free() {
		return "free"
	}

	return fmt.Sprintf("%d", c)
}

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// table renders candidates then rejections, both already ordered.
//
// Ranked candidates come first because the answer is at the top, and
// rejections follow in provider order so that the same plan diffs cleanly
// against itself.
func (c *JobPlanCommand) table(
	ranking scheduler.Ranking, rejections []scheduler.Rejection, verbose bool,
) string {
	var b strings.Builder

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)

	for i := range ranking {
		scored := &ranking[i]

		fmt.Fprintf(w, "%s\tadmitted\tscore %d\t%s\n",
			scored.Provider, scored.Percent(), observedAgo(scored))

		if verbose {
			for _, s := range scored.Scores {
				fmt.Fprintf(w, "\t\t  %s %.2f\n", s.Name, s.Value)
			}
		}
	}

	for i := range rejections {
		r := &rejections[i]

		fmt.Fprintf(w, "%s\trejected\t%s\t%s\n", r.Provider, r.Reason, r.Detail)

		if verbose {
			for _, also := range r.Also {
				fmt.Fprintf(w, "\t\t  %s\n", also)
			}
		}
	}

	_ = w.Flush()

	return strings.TrimRight(b.String(), "\n")
}

// observedAgo says how old the snapshot behind a verdict is.
//
// Printed because a plan is exactly as current as its data, and an operator
// reading one has no other way to know whether it describes the world or
// describes yesterday.
func observedAgo(scored *scheduler.ScoredCandidate) string {
	observed := scored.Capabilities.ObservedAt
	if observed.IsZero() {
		return "never observed"
	}

	return fmt.Sprintf("observed %s", observed.UTC().Format("2006-01-02 15:04:05Z"))
}
