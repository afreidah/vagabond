// -------------------------------------------------------------------------------
// vagabond job status
//
// Author: Alex Freidah
//
// With no name, the jobs registered in a namespace. With one, that job's
// schedule if it has one, its versions and its most recent executions.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// COMMAND
// -------------------------------------------------------------------------

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
  that job's versions and its most recent executions. A periodic job also
  shows its crons, time zone, whether it prohibits overlap, and its next
  launch.

Status Options:

  -namespace <name>
    Defaults to $VAGABOND_NAMESPACE, then "default".
`

	return clientHelp(text)
}

// Run lists the namespace's jobs with no argument, or shows the one job it
// names.
func (c *JobStatusCommand) Run(args []string) int {
	flags := c.FlagSet("job status")
	c.namespaceFlag(flags)

	client, names := c.connect(flags, args, optionalArg("[name]"), c.Help())
	if client == nil {
		return ExitFailure
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

	c.Ui.Output(columns(func(w io.Writer) {
		_, _ = fmt.Fprintln(w, "NAME\tVERSION\tSTATUS\tUPDATED")

		for i := range all {
			j := &all[i]
			_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", j.Name, j.Version, standing(j), stamp(j.Updated))
		}
	}))

	return ExitSuccess
}

// show prints one job's standing and any schedule, its versions, and its
// recent executions.
func (c *JobStatusCommand) show(ctx context.Context, client *api.Client, name string) int {
	status, err := client.JobStatus(ctx, c.namespace, name)
	if err != nil {
		return c.apiFailure(err)
	}

	var b strings.Builder

	b.WriteString(fields(append([][2]string{
		{"Name", status.Name},
		{"Namespace", status.Namespace},
		{"Version", fmt.Sprintf("%d", status.Version)},
		{"Status", standing(&status.Job)},
	}, scheduleFields(&status.Job, time.Now())...)))

	b.WriteString("\n\nVersions\n")
	b.WriteString(columns(func(w io.Writer) {
		_, _ = fmt.Fprintln(w, "VERSION\tREGISTERED")

		for _, v := range status.Versions {
			_, _ = fmt.Fprintf(w, "%d\t%s\n", v.Version, stamp(v.Registered))
		}
	}))

	b.WriteString("\n\nRecent executions\n")
	b.WriteString(executionTable(status.Executions, false))

	c.Ui.Output(b.String())

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

// relative says how far t is from now, to the second: "in 3m0s" when t is
// ahead, "4s ago" otherwise.
func relative(t, now time.Time) string {
	d := t.Sub(now).Round(time.Second)
	if d > 0 {
		return "in " + d.String()
	}

	return (-d).String() + " ago"
}

// when renders a time with how far it is from now, as "2026-11-22 00:00:00Z
// (in 13h59m30s)".
func when(t, now time.Time) string {
	return fmt.Sprintf("%s (%s)", stamp(t), relative(t, now))
}

// scheduleFields renders a periodic job's schedule and next launch as status
// rows, and none for a job that launches only by hand.
func scheduleFields(j *api.Job, now time.Time) [][2]string {
	if j.Periodic == nil {
		return nil
	}

	rows := [][2]string{
		{"Crons", strings.Join(j.Periodic.Crons, ", ")},
		{"Time zone", j.Periodic.TimeZone},
		{"Prohibit overlap", strconv.FormatBool(j.Periodic.ProhibitOverlap)},
	}

	if j.NextLaunch != nil {
		rows = append(rows, [2]string{"Next launch", when(*j.NextLaunch, now)})
	}

	return rows
}
