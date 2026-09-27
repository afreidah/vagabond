// -------------------------------------------------------------------------------
// Reading a Job Specification
//
// Author: Alex Freidah
//
// Every command that takes a job file reads it the same way, including from a
// pipe. A job file sent to a server is validated here first, so its mistakes
// are reported against the source as job validate reports them; the server
// validates it again.
// -------------------------------------------------------------------------------

package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/jobspec"
)

// stdinPath is the argument that means "read the specification from a pipe",
// matching what nomad job validate and nomad job plan accept.
const stdinPath = "-"

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

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

// readSource reads a job file's bytes as written, or standard input for "-",
// for commands that send the source rather than only parse it.
func (m *Meta) readSource(path string) ([]byte, error) {
	if path == stdinPath {
		src, err := io.ReadAll(m.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading standard input: %w", err)
		}

		return src, nil
	}

	src, err := os.ReadFile(path) //nolint:gosec // the operator named this file
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	return src, nil
}

// -------------------------------------------------------------------------
// CHECKING BEFORE SENDING
// -------------------------------------------------------------------------

// checkJob reads a job file and validates it with meta, as it would run, and
// returns its source to send.
func (m *Meta) checkJob(path string, meta metaFlags) ([]byte, int) {
	src, err := m.readSource(path)
	if err != nil {
		return nil, m.Errorf("%s", err)
	}

	parsed, diags := jobs.Load(filename(path), src, meta)

	return m.checked(src, parsed, diags)
}

// checkRegister reads a job file and validates it as register does, with its
// declared metadata standing as written, and returns its source to send.
func (m *Meta) checkRegister(path string) ([]byte, int) {
	src, err := m.readSource(path)
	if err != nil {
		return nil, m.Errorf("%s", err)
	}

	parsed, diags := jobs.ForRegister(filename(path), src)

	return m.checked(src, parsed, diags)
}

// checked renders what validating reported and hands the source back when
// nothing was wrong.
func (m *Meta) checked(src []byte, parsed *jobspec.Parsed, diags hcl.Diagnostics) ([]byte, int) {
	if !diags.HasErrors() {
		return src, ExitSuccess
	}

	renderDiagnostics(m.Ui, parsed.Files(), diags, m.color())

	return nil, ExitFailure
}

// filename is what diagnostics call the input: the path, or nothing for
// standard input.
func filename(path string) string {
	if path == stdinPath {
		return ""
	}

	return path
}
