// -------------------------------------------------------------------------------
// vagabond execution status
//
// Author: Alex Freidah
//
// One execution's record: where it came from, where it ran, what state it
// reached, and its result once it has one.
// -------------------------------------------------------------------------------

package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/afreidah/vagabond/internal/api"
)

// -------------------------------------------------------------------------
// COMMAND
// -------------------------------------------------------------------------

// ExecutionStatusCommand implements `vagabond execution status`.
type ExecutionStatusCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *ExecutionStatusCommand) Synopsis() string {
	return "Show one execution"
}

// Help returns the full usage text.
func (c *ExecutionStatusCommand) Help() string {
	text := `
Usage: vagabond execution status [options] <id>

  Shows one execution's record: its job, task and provider, the state it
  reached, and its result once it has one. Its output is read with execution
  logs.
`

	return clientHelp(text)
}

// Run reads the execution and prints its record.
func (c *ExecutionStatusCommand) Run(args []string) int {
	client, ids := c.connect(c.FlagSet("execution status"), args, oneArg("<id>"), c.Help())
	if client == nil {
		return ExitFailure
	}

	e, err := client.Execution(context.Background(), ids[0])
	if err != nil {
		return c.apiFailure(err)
	}

	c.Ui.Output(record(e))

	return ExitSuccess
}

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// record renders an execution as aligned fields, leaving out what it does not
// have yet.
func record(e *api.Execution) string {
	pairs := [][2]string{
		{"ID", e.ID},
		{"Namespace", e.Namespace},
		{"Job", fmt.Sprintf("%s (version %d)", e.Job, e.JobVersion)},
		{"Dispatch", e.DispatchID},
		{"Task", e.Task},
		{"Provider", e.Provider},
		{"Node", e.Node},
		{"Attempt", fmt.Sprintf("%d", e.Attempt)},
		{"Previous", e.PreviousID},
		{"State", e.State},
		{"Provider ID", e.ProviderID},
		{"Failure", e.Failure},
		{"Created", stamp(e.Created)},
		{"Started", optionalStamp(e.Started)},
		{"Ended", optionalStamp(e.Ended)},
	}

	if e.HasResult {
		pairs = append(pairs, [2]string{"Duration", e.Duration.String()})

		if e.ExitCode != nil {
			pairs = append(pairs, [2]string{"Exit Code", fmt.Sprintf("%d", *e.ExitCode)})
		}

		pairs = append(pairs, [2]string{"Reason", e.Reason})

		if b := e.Billed; b != nil {
			pairs = append(pairs, [2]string{
				"Billed", fmt.Sprintf("%d millicores, %d MiB, %s", b.CPU, b.Memory, b.Duration),
			})
		}
	}

	return fields(pairs)
}

// executionTable renders executions one row each, in the order given, with
// each one's job when withJob is set, or "None." when there are none.
func executionTable(executions []api.Execution, withJob bool) string {
	if len(executions) == 0 {
		return "None."
	}

	return columns(func(w io.Writer) {
		job := func(name string) string {
			if withJob {
				return name + "\t"
			}

			return ""
		}

		_, _ = fmt.Fprintln(w, "ID\t"+job("JOB")+"TASK\tPROVIDER\tNODE\tATTEMPT\tSTATE\tCREATED")

		for i := range executions {
			e := &executions[i]
			_, _ = fmt.Fprintf(w, "%s\t%s%s\t%s\t%s\t%d\t%s\t%s\n",
				e.ID, job(e.Job), e.Task, e.Provider, cmp.Or(e.Node, "-"), e.Attempt, e.State, stamp(e.Created))
		}
	})
}

// optionalStamp renders a time that may not have happened yet as empty.
func optionalStamp(t *time.Time) string {
	if t == nil {
		return ""
	}

	return stamp(*t)
}
