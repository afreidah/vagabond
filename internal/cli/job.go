// -------------------------------------------------------------------------------
// vagabond job
//
// Author: Alex Freidah
//
// The namespace itself, registered as a command so that it carries a synopsis
// in the top-level listing. Without it the library synthesises a placeholder
// with no description, and help shows a bare "job" that tells a reader nothing.
// Nomad registers its namespaces the same way.
// -------------------------------------------------------------------------------

package cli

import "strings"

// JobCommand implements `vagabond job`, which exists to describe its
// subcommands rather than to do anything itself.
type JobCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in the top-level listing.
func (c *JobCommand) Synopsis() string {
	return "Interact with jobs"
}

// Help returns the namespace usage text.
func (c *JobCommand) Help() string {
	text := `
Usage: vagabond job <subcommand> [options] [args]

  Interact with job specifications. A job describes what to run; Vagabond
  decides where it runs.

  Check a job file for errors:

      $ vagabond job validate ./example.vagabond.hcl

  Please see the individual subcommand help for detailed usage information.
`

	return strings.TrimSpace(text)
}

// Run prints help, because a namespace on its own is not an instruction.
func (c *JobCommand) Run(_ []string) int {
	return c.Errorf("%s", c.Help())
}
