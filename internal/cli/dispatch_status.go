// -------------------------------------------------------------------------------
// vagabond dispatch status
//
// Author: Alex Freidah
//
// One run's record: its job, the state it reached and why when it got no
// answer, and every execution it created.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/afreidah/vagabond/internal/api"
)

// -------------------------------------------------------------------------
// COMMAND
// -------------------------------------------------------------------------

// DispatchStatusCommand implements `vagabond dispatch status`.
type DispatchStatusCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *DispatchStatusCommand) Synopsis() string {
	return "Show one dispatch and its executions"
}

// Help returns the full usage text.
func (c *DispatchStatusCommand) Help() string {
	text := `
Usage: vagabond dispatch status [options] <id>

  Shows one dispatch, the run of a job that job run and job dispatch start and
  name first: its job and version, the state it reached and why when it got
  no answer, and every execution it created, oldest first.
`

	return clientHelp(text)
}

// Run reads the dispatch and prints its record and executions.
func (c *DispatchStatusCommand) Run(args []string) int {
	client, ids := c.connect(c.FlagSet("dispatch status"), args, oneArg("<id>"), c.Help())
	if client == nil {
		return ExitFailure
	}

	d, err := client.DispatchStatus(context.Background(), ids[0])
	if err != nil {
		return c.apiFailure(err)
	}

	c.Ui.Output(dispatchRecord(d))

	return ExitSuccess
}

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// dispatchRecord renders a dispatch as aligned fields, then its executions.
func dispatchRecord(d *api.Dispatch) string {
	var b strings.Builder

	b.WriteString(fields([][2]string{
		{"ID", d.DispatchID},
		{"Namespace", d.Namespace},
		{"Job", fmt.Sprintf("%s (version %d)", d.Job, d.JobVersion)},
		{"State", d.State},
		{"Error", d.Error},
		{"Created", stamp(d.Created)},
		{"Ended", optionalStamp(d.Ended)},
	}))

	b.WriteString("\n\nExecutions\n")
	b.WriteString(executionTable(d.Executions, false))

	return b.String()
}
