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
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/api"
)

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

Status Options:

  -address <addr>
    The server to read. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.
`

	return strings.TrimSpace(text)
}

// Run reads the execution and prints its record.
func (c *ExecutionStatusCommand) Run(args []string) int {
	flags := c.FlagSet("execution status")
	c.clientFlags(flags)

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	ids := flags.Args()
	if len(ids) != 1 {
		return c.Errorf("This command takes one argument: <id>\n\n%s", c.Help())
	}

	client, err := c.client()
	if err != nil {
		return c.Errorf("%s", err)
	}

	e, err := client.Execution(context.Background(), ids[0])
	if err != nil {
		return c.apiFailure(err)
	}

	c.Ui.Output(record(e))

	return ExitSuccess
}

// record renders an execution as aligned fields, leaving out what it does not
// have yet.
func record(e *api.Execution) string {
	fields := [][2]string{
		{"ID", e.ID},
		{"Namespace", e.Namespace},
		{"Job", fmt.Sprintf("%s (version %d)", e.Job, e.JobVersion)},
		{"Dispatch", e.DispatchID},
		{"Task", e.Task},
		{"Provider", e.Provider},
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
		fields = append(fields, [2]string{"Duration", e.Duration.String()})

		if e.ExitCode != nil {
			fields = append(fields, [2]string{"Exit Code", fmt.Sprintf("%d", *e.ExitCode)})
		}

		if b := e.Billed; b != nil {
			fields = append(fields, [2]string{
				"Billed", fmt.Sprintf("%d millicores, %d MiB, %s", b.CPU, b.Memory, b.Duration),
			})
		}
	}

	var out strings.Builder

	for _, f := range fields {
		if f[1] != "" {
			_, _ = fmt.Fprintf(&out, "%-11s = %s\n", f[0], f[1])
		}
	}

	return strings.TrimRight(out.String(), "\n")
}

// optionalStamp renders a time that may not have happened yet as empty.
func optionalStamp(t *time.Time) string {
	if t == nil {
		return ""
	}

	return stamp(*t)
}
