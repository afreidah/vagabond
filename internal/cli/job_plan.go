// -------------------------------------------------------------------------------
// vagabond job plan
//
// Author: Alex Freidah
//
// Answers the question validation cannot: given the providers the server has
// right now, where would this job run, and why not everywhere else.
//
// The server plans from capability and quota snapshots gathered beforehand, so
// no provider is contacted and a plan is exactly as current as its data, which
// it says.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/afreidah/vagabond/internal/api"
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
Usage: vagabond job plan [options] <path or name>

  Admits a job against the server's providers and shows the result: which
  could run it, how each scored, and for every one that could not, the reason.

  Nothing is dispatched and nothing is reserved. Planning the same job twice
  changes nothing and costs nothing.

  An argument that exists as a file is read as one, and "-" reads standard
  input. Anything else names a registered job, whose current version is
  planned with its metadata checked as dispatch checks it.

  No provider is contacted. The server plans from capability snapshots
  gathered earlier, so a plan is only as current as its data and can be wrong
  by the age of it. Each provider's observation time is shown.

  Plan will return one of the following exit codes:
    * 0: At least one provider can run every task.
    * 1: Some task has nowhere to run, or the plan could not be produced.

Plan Options:

  -address <addr>
    The server to plan on. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.

  -meta <key>=<value>
    Supply job metadata, repeatable. Values are substituted into the job before
    it is planned, so a specification that interpolates meta.version is planned
    as it would actually be submitted.

  -namespace <name>
    The namespace to plan in, for a job that names none. Defaults to
    $VAGABOND_NAMESPACE, then "default". A job naming a different one is an
    error.

  -verbose
    Show every scorer's contribution rather than the final score alone.
`

	return strings.TrimSpace(text)
}

// Run plans the named job file or registered job on the server.
func (c *JobPlanCommand) Run(args []string) int {
	var (
		meta    metaFlags
		verbose bool
	)

	flags := c.FlagSet("job plan")
	c.clientFlags(flags)
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")
	flags.BoolVar(&verbose, "verbose", false, "show each scorer's contribution")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	paths := flags.Args()
	if len(paths) != 1 {
		return c.Errorf("This command takes one argument: <path or name>\n\n%s", c.Help())
	}

	req := api.PlanRequest{Name: paths[0], Meta: meta}

	if isFile(paths[0]) {
		src, code := c.checkJob(paths[0], meta)
		if src == nil {
			return code
		}

		req = api.PlanRequest{Source: string(src), Meta: meta}
	}

	client, err := c.client()
	if err != nil {
		return c.Errorf("%s", err)
	}

	plan, err := client.Plan(context.Background(), c.namespace, req)
	if err != nil {
		return c.apiFailure(err)
	}

	return c.render(plan, verbose)
}

// isFile reports whether arg names a job file rather than a registered job:
// standard input, anything shaped like a path, or a path that exists. A
// mistyped file name is then reported as a missing file, not a missing job.
func isFile(arg string) bool {
	if arg == stdinPath || strings.ContainsRune(arg, os.PathSeparator) || filepath.Ext(arg) == ".hcl" {
		return true
	}

	_, err := os.Stat(arg)

	return err == nil
}

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// render prints every task's plan, a blank line between tasks, and returns
// failure when any task has nowhere to run.
func (c *JobPlanCommand) render(plan *api.Plan, verbose bool) int {
	code := ExitSuccess

	for i := range plan.Tasks {
		if i > 0 {
			c.Ui.Output("")
		}

		if !c.renderTask(&plan.Tasks[i], verbose) {
			code = ExitFailure
		}
	}

	return code
}

// renderTask prints one task's plan and reports whether anything can run it.
func (c *JobPlanCommand) renderTask(t *api.TaskPlan, verbose bool) bool {
	c.Ui.Output(fmt.Sprintf("%s.%s (%s)", t.Job, t.Task, t.Driver))
	c.Ui.Output(table(t, verbose))

	if t.Selected == "" {
		c.Ui.Output("No provider can run this task.")

		if t.Retryable {
			c.Ui.Output("At least one rejection is transient, so this may be admitted later.")
		}

		return false
	}

	c.Ui.Output(fmt.Sprintf("Selected: %s", t.Selected))
	c.Ui.Output(fmt.Sprintf("Estimated cost: %s", cost(t.EstimatedCost)))

	return true
}

// table renders candidates then rejections, both already ordered: ranked
// candidates first because the answer is at the top, rejections in provider
// order so the same plan diffs cleanly against itself.
func table(t *api.TaskPlan, verbose bool) string {
	var b strings.Builder

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)

	for i := range t.Candidates {
		cand := &t.Candidates[i]

		_, _ = fmt.Fprintf(w, "%s\tadmitted\tscore %d\t%s\n", cand.Provider, cand.Score, observed(cand))

		if verbose {
			for _, s := range cand.Scores {
				_, _ = fmt.Fprintf(w, "\t\t  %s %.2f\n", s.Name, s.Value)
			}
		}
	}

	for i := range t.Rejections {
		r := &t.Rejections[i]

		_, _ = fmt.Fprintf(w, "%s\trejected\t%s\t%s\n", r.Provider, r.Reason, r.Detail)

		if verbose {
			for _, also := range r.Also {
				_, _ = fmt.Fprintf(w, "\t\t  %s\n", also)
			}
		}
	}

	_ = w.Flush()

	return strings.TrimRight(b.String(), "\n")
}

// observed says how old the snapshot behind a verdict is, since a plan is
// exactly as current as its data.
func observed(cand *api.Candidate) string {
	if cand.Observed.IsZero() {
		return "never observed"
	}

	return fmt.Sprintf("observed %s", stamp(cand.Observed))
}

// cost renders what an execution would be charged. Free is spelled rather than
// printed as a zero, and a non-zero cost prints as a bare number, since its
// unit is not pinned yet.
func cost(c int64) string {
	if c == 0 {
		return "free"
	}

	return fmt.Sprintf("%d", c)
}
