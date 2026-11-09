// -------------------------------------------------------------------------------
// vagabond job validate
//
// Author: Alex Freidah
//
// Checks that a job file is well formed and means something, without contacting
// any provider. Validation is provider-independent by definition: whether
// anything can actually run the job is admission's question, and answering it
// needs a capability snapshot this command deliberately does not have.
// -------------------------------------------------------------------------------

package cli

import (
	"fmt"
	"strings"

	"github.com/afreidah/vagabond/internal/jobspec"
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
  and rule violations. Syntax problems are shown with the offending line; rule
  violations name the job and task they apply to.

  Reads from standard input when the path is "-".

  Validation never contacts a provider. Whether any provider can currently run
  the job is a separate question, answered by "vagabond job plan".

  Exits zero when the specification is valid, and non-zero otherwise.

Validate Options:

  -meta <key>=<value>
    Supply job metadata, repeatable. Values are substituted into the job before
    it is checked, so a specification that interpolates meta.version is
    validated as it would actually be submitted.

    A job declaring meta_required is refused when a value is not supplied,
    rather than validating and then interpolating nothing at submission.
`

	return strings.TrimSpace(text)
}

// Run parses and validates the named specification.
func (c *JobValidateCommand) Run(args []string) int {
	var meta metaFlags

	flags := c.FlagSet("job validate")
	flags.Var(&meta, "meta", "job metadata as key=value, repeatable")

	paths, ok := c.parse(flags, args, oneArg("<path>"), c.Help())
	if !ok {
		return ExitFailure
	}

	parsed, diags := c.parseJob(paths[0], meta)

	// Rules on a file that failed to decode would only flag unpopulated fields
	if !diags.HasErrors() {
		diags = append(diags, jobspec.Validate(parsed.Spec)...)
	}

	if diags.HasErrors() {
		renderDiagnostics(c.Ui, parsed.Files(), diags, c.color())

		return ExitFailure
	}

	c.Ui.Output(fmt.Sprintf("%s is valid.", describe(paths[0])))

	return ExitSuccess
}

// describe names the input for a success message, since "- is valid" reads
// oddly.
func describe(path string) string {
	if path == stdinPath {
		return "The specification"
	}

	return "Job specification " + path
}
