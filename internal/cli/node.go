// -------------------------------------------------------------------------------
// vagabond node
//
// Author: Alex Freidah
//
// The namespace for client nodes, registered as a command so it carries a
// synopsis in the top-level listing, as job does.
// -------------------------------------------------------------------------------

package cli

import "strings"

// NodeCommand implements `vagabond node`, which exists to describe its
// subcommands rather than to do anything itself.
type NodeCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in the top-level listing.
func (c *NodeCommand) Synopsis() string {
	return "Interact with client nodes"
}

// Help returns the namespace usage text.
func (c *NodeCommand) Help() string {
	text := `
Usage: vagabond node <subcommand> [options] [args]

  Read the client nodes connected to a server: the machines vagabond agent
  runs workloads on.

  List the connected nodes:

      $ vagabond node status

  Please see the individual subcommand help for detailed usage information.
`

	return strings.TrimSpace(text)
}

// Run prints help, because a namespace on its own is not an instruction.
func (c *NodeCommand) Run(_ []string) int {
	return c.Errorf("%s", c.Help())
}
