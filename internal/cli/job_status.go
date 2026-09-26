// -------------------------------------------------------------------------------
// vagabond job status
//
// Author: Alex Freidah
//
// With no name, the jobs registered in a namespace. With one, that job's
// versions and its most recent executions with the dispatch each belongs to.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/job"
)

// JobStatusCommand implements `vagabond job status`.
type JobStatusCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *JobStatusCommand) Synopsis() string {
	return "List registered jobs, or show one"
}

// Help returns the full usage text.
func (c *JobStatusCommand) Help() string {
	text := `
Usage: vagabond job status [options] [name]

  With no name, lists the jobs registered in the namespace. With a name, shows
  that job's versions and its most recent executions.

Status Options:

  -address <addr>
    The server to read. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.

  -namespace <name>
    Defaults to $VAGABOND_NAMESPACE, then "default".
`

	return strings.TrimSpace(text)
}

// Run lists the namespace's jobs with no argument, or shows the one job it
// names.
func (c *JobStatusCommand) Run(args []string) int {
	flags := c.FlagSet("job status")
	c.clientFlags(flags)

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	names := flags.Args()
	if len(names) > 1 {
		return c.Errorf("This command takes at most one argument: [name]\n\n%s", c.Help())
	}

	client, err := c.client()
	if err != nil {
		return c.Errorf("%s", err)
	}

	if len(names) == 0 {
		return c.list(context.Background(), client)
	}

	return c.show(context.Background(), client, names[0])
}

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// list prints every job in the namespace as a table, stopped ones included, or
// says there are none.
func (c *JobStatusCommand) list(ctx context.Context, client *api.Client) int {
	all, err := client.Jobs(ctx, c.namespace)
	if err != nil {
		return c.apiFailure(err)
	}

	if len(all) == 0 {
		ns := c.namespace
		if ns == "" {
			ns = job.DefaultNamespace
		}

		c.Ui.Output(fmt.Sprintf("No jobs are registered in namespace %q.", ns))

		return ExitSuccess
	}

	var b bytes.Buffer

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tVERSION\tSTATUS\tUPDATED")

	for i := range all {
		j := &all[i]
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", j.Name, j.Version, standing(j), stamp(j.Updated))
	}

	_ = w.Flush()
	c.Ui.Output(strings.TrimRight(b.String(), "\n"))

	return ExitSuccess
}

// show prints one job's standing, its versions, and its recent executions with
// the dispatch each belongs to.
func (c *JobStatusCommand) show(ctx context.Context, client *api.Client, name string) int {
	status, err := client.JobStatus(ctx, c.namespace, name)
	if err != nil {
		return c.apiFailure(err)
	}

	var b bytes.Buffer

	_, _ = fmt.Fprintf(&b, "Name      = %s\nNamespace = %s\nVersion   = %d\nStatus    = %s\n\nVersions\n",
		status.Name, status.Namespace, status.Version, standing(&status.Job))

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "VERSION\tREGISTERED")

	for _, v := range status.Versions {
		_, _ = fmt.Fprintf(w, "%d\t%s\n", v.Version, stamp(v.Registered))
	}

	_ = w.Flush()

	b.WriteString("\nRecent executions\n")

	if len(status.Executions) == 0 {
		b.WriteString("None.\n")
	} else {
		w = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "DISPATCH\tVERSION\tTASK\tPROVIDER\tSTATE\tCREATED")

		for i := range status.Executions {
			e := &status.Executions[i]
			_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n",
				short(e.DispatchID), e.JobVersion, e.Task, e.Provider, e.State, stamp(e.Created))
		}

		_ = w.Flush()
	}

	c.Ui.Output(strings.TrimRight(b.String(), "\n"))

	return ExitSuccess
}

// standing renders whether a job can be dispatched: registered, or stopped
// until registered again.
func standing(j *api.Job) string {
	if j.Stopped {
		return "stopped"
	}

	return "registered"
}

// stamp renders a time for a table, in UTC so every reader sees the same.
func stamp(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05Z")
}

// short keeps the last eight characters of an ID. IDs are UUIDv7, whose leading
// characters are a timestamp that two runs a moment apart share.
func short(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}

	return id
}
