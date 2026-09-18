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
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"

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

	file, diags := jobspec.ParseFile(paths[0], meta)

	// Only validate what parsed. Rules run against the decoded specification,
	// so a file that failed to decode would produce a second wave of complaints
	// about fields that were never populated.
	if !diags.HasErrors() {
		diags = append(diags, jobspec.Validate(file)...)
	}

	if diags.HasErrors() {
		return c.reportDiagnostics(diags)
	}

	c.Ui.Output(fmt.Sprintf("Job specification %s is valid.", paths[0]))

	return ExitSuccess
}

// reportDiagnostics prints every problem found, not just the first.
//
// hcl.Diagnostics.Error summarises as "the first one, and N others", which is
// the opposite of what a validator is for: an author wants the whole list so
// they can fix a file in one pass.
//
// Deliberately plain. The diagnostic writer that renders a source excerpt with
// the offending line underneath replaces this.
func (c *JobValidateCommand) reportDiagnostics(diags hcl.Diagnostics) int {
	for _, d := range diags {
		c.Ui.Error(formatDiagnostic(d))
	}

	return ExitFailure
}

// formatDiagnostic renders one diagnostic, with its position when it has one.
//
// Validation rules run against the decoded specification, which carries no
// source ranges, so those name the job and task in the message instead. Parse
// diagnostics do have a position, and printing "<nil>" for the ones that do not
// would be worse than omitting it.
func formatDiagnostic(d *hcl.Diagnostic) string {
	var b strings.Builder

	if d.Subject != nil {
		fmt.Fprintf(&b, "%s: ", d.Subject)
	}

	b.WriteString(d.Summary)

	if d.Detail != "" {
		b.WriteString("\n  ")
		b.WriteString(d.Detail)
	}

	return b.String()
}
