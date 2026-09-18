// -------------------------------------------------------------------------------
// Diagnostic Rendering
//
// Author: Alex Freidah
//
// An error saying "invalid job" is a validator people stop running. One that
// points at line 47, shows the line, and underlines the offending expression is
// one they keep. HCL carries source ranges through parsing for exactly this,
// and its own writer is what Terraform's output is built on.
//
// Not every diagnostic has a range. Validation rules run against the decoded
// specification, which carries none, so those are rendered as plain text rather
// than with a position of "<nil>".
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hashicorp/cli"
	"github.com/hashicorp/hcl/v2"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// defaultWidth is where the writer wraps detail text.
//
// Fixed rather than read from the terminal, because the output is as likely to
// land in a CI log as on a screen, and a log wrapped to whatever width the last
// machine happened to have is worse than one wrapped predictably.
const defaultWidth = 78

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// renderDiagnostics writes every problem found, not just the first.
//
// hcl.Diagnostics.Error summarises as one diagnostic and a count of the rest,
// which is the opposite of what a validator is for: an author wants the whole
// list so they can fix a file in one pass.
func renderDiagnostics(ui cli.Ui, files map[string]*hcl.File, diags hcl.Diagnostics, color bool) {
	positioned, plain := splitByPosition(diags)

	for _, text := range renderPositioned(files, positioned, color) {
		ui.Error(text)
	}

	for _, d := range plain {
		ui.Error(formatDiagnostic(d))
	}
}

// splitByPosition separates the diagnostics hcl's writer can render from the
// ones it cannot.
//
// A diagnostic whose range names a file the writer was not given renders
// without an excerpt and with a confusing header, so it is treated as plain.
func splitByPosition(diags hcl.Diagnostics) (positioned, plain hcl.Diagnostics) {
	for _, d := range diags {
		if d.Subject != nil {
			positioned = append(positioned, d)

			continue
		}

		plain = append(plain, d)
	}

	return positioned, plain
}

// renderPositioned runs hcl's own writer over the diagnostics that have a
// range, returning one string per diagnostic.
//
// Written one at a time into a buffer rather than all at once, so that the UI
// receives them as separate messages and a prefixing UI can mark each.
func renderPositioned(files map[string]*hcl.File, diags hcl.Diagnostics, color bool) []string {
	if len(diags) == 0 {
		return nil
	}

	out := make([]string, 0, len(diags))

	for _, d := range diags {
		var buf bytes.Buffer

		writer := hcl.NewDiagnosticTextWriter(&buf, files, defaultWidth, color)
		if err := writer.WriteDiagnostic(d); err != nil {
			// The writer only fails on a broken destination, and the
			// destination here is a buffer. Fall back rather than lose the
			// diagnostic entirely.
			out = append(out, formatDiagnostic(d))

			continue
		}

		out = append(out, strings.TrimRight(buf.String(), "\n"))
	}

	return out
}

// formatDiagnostic renders one diagnostic without a source excerpt.
//
// Used for validation rules, which run against the decoded specification and so
// have nothing to point at. The message names the job and task instead.
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

// -------------------------------------------------------------------------
// COLOUR
// -------------------------------------------------------------------------

// useColor reports whether output should carry escape sequences.
//
// Honours NO_COLOR, which is worth the two lines: its absence is noticed
// immediately by anyone piping output into a file or a CI log.
func useColor(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}

	return isTerminal(w)
}

// isTerminal reports whether w is a character device.
//
// Deliberately stdlib. A terminal detection library would be a dependency in
// the supply chain of a tool that runs other people's code, bought for one
// question about a file mode.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}

	info, err := f.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}
