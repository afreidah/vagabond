// -------------------------------------------------------------------------------
// vagabond job status
//
// Author: Alex Freidah
//
// With no name, the jobs registered in a namespace. With one, that job's
// versions and its most recent executions, grouped by dispatch.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/afreidah/vagabond/internal/jobs"
)

// statusExecutions is how many recent executions job status shows.
const statusExecutions = 20

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

  Requires a store block.

Status Options:

  -config <path>
    Configuration file or directory. Defaults as for job run.

  -namespace <name>
    Defaults to $VAGABOND_NAMESPACE, then "default".
`

	return strings.TrimSpace(text)
}

// Run lists or shows.
func (c *JobStatusCommand) Run(args []string) int {
	var configPath, namespace string

	flags := c.FlagSet("job status")
	flags.StringVar(&configPath, "config", "", "provider configuration file or directory")
	flags.StringVar(&namespace, "namespace", os.Getenv(namespaceEnv), "namespace to read")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	names := flags.Args()
	if len(names) > 1 {
		return c.Errorf("This command takes at most one argument: [name]\n\n%s", c.Help())
	}

	ctx := context.Background()

	reg, s, finish, code := c.loadJobStores(ctx, configPath, "job status")
	if reg == nil {
		return code
	}

	defer finish()

	ns, err := namespaceOf(namespace, reg)
	if err != nil {
		return c.Errorf("%s", err)
	}

	if len(names) == 0 {
		return c.list(ctx, s, ns)
	}

	return c.show(ctx, s, ns, names[0])
}

// list prints every job in the namespace.
func (c *JobStatusCommand) list(ctx context.Context, s *stores, namespace string) int {
	all, err := s.jobs.Jobs(ctx, namespace)
	if err != nil {
		return c.Errorf("%s", err)
	}

	if len(all) == 0 {
		c.Ui.Output(fmt.Sprintf("No jobs are registered in namespace %q.", namespace))

		return ExitSuccess
	}

	var b bytes.Buffer

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tVERSION\tSTATUS\tUPDATED")

	for _, j := range all {
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", j.Name, j.Version, standing(j), stamp(j.Updated))
	}

	_ = w.Flush()
	c.Ui.Output(strings.TrimRight(b.String(), "\n"))

	return ExitSuccess
}

// show prints one job, its versions, and its recent executions.
func (c *JobStatusCommand) show(ctx context.Context, s *stores, namespace, name string) int {
	j, err := s.jobs.Job(ctx, namespace, name)
	if err != nil {
		return c.Errorf("%s", err)
	}

	versions, err := s.jobs.Versions(ctx, namespace, name)
	if err != nil {
		return c.Errorf("%s", err)
	}

	runs, err := s.jobs.JobExecutions(ctx, namespace, name, statusExecutions)
	if err != nil {
		return c.Errorf("%s", err)
	}

	var b bytes.Buffer

	_, _ = fmt.Fprintf(&b, "Name      = %s\nNamespace = %s\nVersion   = %d\nStatus    = %s\n\nVersions\n",
		j.Name, j.Namespace, j.Version, standing(j))

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "VERSION\tREGISTERED")

	for _, v := range versions {
		_, _ = fmt.Fprintf(w, "%d\t%s\n", v.Version, stamp(v.Created))
	}

	_ = w.Flush()

	b.WriteString("\nRecent executions\n")

	if len(runs) == 0 {
		b.WriteString("None.\n")
	} else {
		w = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "DISPATCH\tVERSION\tTASK\tPROVIDER\tSTATE\tCREATED")

		for _, r := range runs {
			_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n",
				short(r.Dispatch.String()), r.JobVersion, r.Task, r.Provider, r.State, stamp(r.ID.Created()))
		}

		_ = w.Flush()
	}

	c.Ui.Output(strings.TrimRight(b.String(), "\n"))

	return ExitSuccess
}

// standing renders whether a job can be dispatched.
func standing(j *jobs.Job) string {
	if j.Stopped {
		return "stopped"
	}

	return "registered"
}

// stamp renders a time for a table, in UTC so every reader sees the same.
func stamp(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05Z")
}

// short keeps the last eight characters of an ID. Nomad shows the first eight,
// but its IDs are random throughout; ours are UUIDv7, whose leading characters
// are a timestamp that two runs a moment apart share.
func short(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}

	return id
}
