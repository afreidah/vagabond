// -------------------------------------------------------------------------------
// Command Groups
//
// Author: Alex Freidah
//
// The words that only group subcommands: job, node, provider, execution and
// dispatch. Each is
// registered as a command so it carries a synopsis in the top-level listing.
// Without one the library synthesises a placeholder, and help shows a bare word
// that tells a reader nothing.
// -------------------------------------------------------------------------------

package cli

import (
	"strings"

	"github.com/hashicorp/cli"
)

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
  much of each quota pool has been used. Turn one off and on without editing
  the configuration.

  List the providers and their usage:

      $ vagabond provider status

  Stop new work going to a provider, and let it go there again:

      $ vagabond provider disable gcp-cloud-run
      $ vagabond provider enable gcp-cloud-run

  Please see the individual subcommand help for detailed usage information.
`

	executionHelp = `
Usage: vagabond execution <subcommand> [options] [args]

  Read executions: each one attempt of one task on one provider.

  List the most recent executions:

      $ vagabond execution list

  Show an execution's record:

      $ vagabond execution status <id>

  Please see the individual subcommand help for detailed usage information.
`

	dispatchHelp = `
Usage: vagabond dispatch <subcommand> [options] [args]

  Read dispatches: each one run of a job, started by job run or job dispatch,
  and the executions it created.

  Show a dispatch and its executions:

      $ vagabond dispatch status <id>

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

// Run asks for help, because a group on its own is not an instruction; the
// library prints it with the group's subcommands listed.
func (c *groupCommand) Run(_ []string) int {
	return cli.RunResultHelp
}
