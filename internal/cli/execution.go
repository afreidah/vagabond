// -------------------------------------------------------------------------------
// vagabond execution
//
// Author: Alex Freidah
//
// The namespace for reading single executions, registered as a command so it
// carries a synopsis in the top-level listing, as job does.
// -------------------------------------------------------------------------------

package cli

import "strings"

// ExecutionCommand implements `vagabond execution`, which exists to describe
// its subcommands rather than to do anything itself.
type ExecutionCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in the top-level listing.
func (c *ExecutionCommand) Synopsis() string {
	return "Interact with executions"
}

// Help returns the namespace usage text.
func (c *ExecutionCommand) Help() string {
	text := `
Usage: vagabond execution <subcommand> [options] [args]

  Read one execution: one attempt of one task on one provider.

  Show an execution's record:

      $ vagabond execution status <id>

  Please see the individual subcommand help for detailed usage information.
`

	return strings.TrimSpace(text)
}

// Run prints help, because a namespace on its own is not an instruction.
func (c *ExecutionCommand) Run(_ []string) int {
	return c.Errorf("%s", c.Help())
}
