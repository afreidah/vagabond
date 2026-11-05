// -------------------------------------------------------------------------------
// Command Groups
//
// Author: Alex Freidah
//
// The words that only group subcommands: job, node, provider, execution. Each is
// registered as a command so it carries a synopsis in the top-level listing.
// Without one the library synthesises a placeholder, and help shows a bare word
// that tells a reader nothing.
// -------------------------------------------------------------------------------

package cli

import "strings"

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

const (
	jobHelp = `
Usage: vagabond job <subcommand> [options] [args]

  Interact with job specifications. A job describes what to run; Vagabond
  decides where it runs.

  Check a job file for errors:

      $ vagabond job validate ./example.vagabond.hcl

  Please see the individual subcommand help for detailed usage information.
`

	nodeHelp = `
Usage: vagabond node <subcommand> [options] [args]

  Read the nodes connected to a server: the machines running vagabond agent,
  which the server sends workloads to.

  List the connected nodes:

      $ vagabond node status

  Please see the individual subcommand help for detailed usage information.
`

	providerHelp = `
Usage: vagabond provider <subcommand> [options] [args]

  Read the providers a server is configured with: where jobs can run, and how
  much of each quota pool has been used.

  List the providers and their usage:

      $ vagabond provider status

  Please see the individual subcommand help for detailed usage information.
`

	executionHelp = `
Usage: vagabond execution <subcommand> [options] [args]

  Read one execution: one attempt of one task on one provider.

  Show an execution's record:

      $ vagabond execution status <id>

  Please see the individual subcommand help for detailed usage information.
`
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// groupCommand is a word that exists to describe its subcommands rather than
// to do anything itself.
type groupCommand struct {
	*Meta
	synopsis string
	help     string
}

// Synopsis returns the one-line description shown in the top-level listing.
func (c *groupCommand) Synopsis() string {
	return c.synopsis
}

// Help returns the group's usage text.
func (c *groupCommand) Help() string {
	return strings.TrimSpace(c.help)
}

// Run prints help, because a group on its own is not an instruction.
func (c *groupCommand) Run(_ []string) int {
	return c.Errorf("%s", c.Help())
}
