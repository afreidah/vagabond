// -------------------------------------------------------------------------------
// vagabond job validate
//
// Author: Alex Freidah
//
// Checks that a job file is well formed and means something, without contacting
// any provider. Validation is provider-independent by definition: whether
// anything can actually run the job is admission's question, and answering it
// needs a capability snapshot this command deliberately does not have.
//
// Argument handling and the command contract are settled here. The parsing and
// the rules arrive with the rest of Chunk 2.
// -------------------------------------------------------------------------------

package cli

import (
	"strings"
)

// JobValidateCommand implements `vagabond job validate`.
type JobValidateCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *JobValidateCommand) Synopsis() string {
	return "Check a job specification for errors"
}

// Help returns the full usage text.
func (c *JobValidateCommand) Help() string {
	text := `
Usage: vagabond job validate [options] <path>

  Checks whether a job file is a valid specification, reporting syntax errors
  and rule violations with the line and column they occur on. Reads from stdin
  when the path is "-".

  Validation never contacts a provider. Whether any provider can currently run
  the job is a separate question, answered by "vagabond job plan".

Validate Options:

  -meta <key>=<value>
    Supply job metadata, repeatable. Values are substituted into the job before
    it is checked, so a specification that interpolates meta.git_ref can be
    validated as it would actually be submitted.
`

	return strings.TrimSpace(text)
}

// Run parses arguments and validates the named file.
func (c *JobValidateCommand) Run(args []string) int {
	var meta metaFlags

	flags := c.FlagSet("job validate")
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	paths := flags.Args()
	if len(paths) != 1 {
		return c.Errorf("This command takes one argument: <path>\n\n%s", c.Help())
	}

	return c.Errorf("job validate is not implemented yet")
}
