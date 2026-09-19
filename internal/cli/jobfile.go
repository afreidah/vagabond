// -------------------------------------------------------------------------------
// Reading a Job Specification
//
// Author: Alex Freidah
//
// Every command that takes a job file reads it the same way, including from a
// pipe. Shared rather than duplicated because the alternative is one command
// accepting "-" and another treating it as a filename, which is the kind of
// inconsistency nobody reports and everybody works around.
// -------------------------------------------------------------------------------

package cli

import (
	"fmt"
	"io"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/jobspec"
)

// stdinPath is the argument that means "read the specification from a pipe",
// matching what nomad job validate and nomad job plan accept.
const stdinPath = "-"

// parseJob reads a specification from a file or from standard input.
func (m *Meta) parseJob(path string, meta metaFlags) (*jobspec.Parsed, hcl.Diagnostics) {
	if path != stdinPath {
		return jobspec.ParseFile(path, meta)
	}

	src, err := io.ReadAll(m.Stdin)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Cannot read standard input",
			Detail:   fmt.Sprintf("Reading the specification: %s.", err),
		}}
	}

	return jobspec.Parse(jobspec.Config{Source: src, Meta: meta})
}
