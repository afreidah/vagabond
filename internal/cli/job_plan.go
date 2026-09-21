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
	"strings"
	"text/tabwriter"

	"github.com/afreidah/vagabond/internal/config"
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

  No provider is contacted. Admission reads capability and quota snapshots
  gathered earlier, so a plan is only as current as its data and can be wrong
  by the age of it. Each provider's observation time is shown.

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
		verbose    bool
	)

	flags := c.FlagSet("job plan")
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")
	flags.StringVar(&configPath, "config", "", "provider configuration file or directory")
	flags.BoolVar(&verbose, "verbose", false, "show each scorer's contribution")

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

	reg, code := c.loadRegistry(configPath)
	if reg == nil {
		return code
	}

	return c.plan(spec, meta, reg, verbose)
}

// -------------------------------------------------------------------------
// LOADING
// -------------------------------------------------------------------------

// loadJob parses and validates the specification, reporting as job validate
// does so that the same mistake reads the same way in both commands.
func (c *JobPlanCommand) loadJob(path string, meta metaFlags) (*job.File, int) {
	parsed, diags := c.parseJob(path, meta)

	if !diags.HasErrors() {
		diags = append(diags, jobspec.Validate(parsed.Spec)...)
	}

	if diags.HasErrors() {
		renderDiagnostics(c.Ui, parsed.Files(), diags, c.color())

		return nil, ExitFailure
	}

	return parsed.Spec, ExitSuccess
}

// loadRegistry finds configuration, builds the providers, and refreshes them.
//
// A refresh failure is reported but does not stop the plan. A provider that
// did not answer is marked unhealthy and rejected by name, which is more useful
// than refusing to plan at all: the other providers still have answers.
func (c *JobPlanCommand) loadRegistry(configPath string) (*registry.Registry, int) {
	path, err := config.Discover(configPath)
	if err != nil {
		return nil, c.Errorf("%s", err)
	}

	cfg, diags := config.LoadPath(path)
	if diags.HasErrors() {
		renderDiagnostics(c.Ui, nil, diags, c.color())

		return nil, ExitFailure
	}

	ctx := context.Background()

	reg, diags := registry.New(ctx, cfg)
	if diags.HasErrors() {
		renderDiagnostics(c.Ui, nil, diags, c.color())

		return nil, ExitFailure
	}

	if err := reg.Refresh(ctx); err != nil {
		c.Ui.Warn(fmt.Sprintf("Some providers did not answer: %s", err))
	}

	return reg, ExitSuccess
}

// -------------------------------------------------------------------------
// PLANNING
// -------------------------------------------------------------------------

// plan admits and ranks every task in the specification.
func (c *JobPlanCommand) plan(
	spec *job.File, meta metaFlags, reg *registry.Registry, verbose bool,
) int {
	inputs := reg.Inputs()
	if len(inputs) == 0 {
		return c.Errorf("No providers are configured, so there is nothing to plan against.")
	}

	ctx := jobspec.EvalContext(meta)

	admitted := true
	first := true

	for i := range spec.Jobs {
		j := &spec.Jobs[i]

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
