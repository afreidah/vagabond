// -------------------------------------------------------------------------------
// Diagnostic Rendering
//
// Author: Alex Freidah
//
// Renders HCL diagnostics through HCL's own writer, which shows the offending
// line and underlines the expression. Validation rules run against the decoded
// specification, which carries no ranges, so those render as plain text rather
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

// defaultWidth is where the writer wraps detail text. Fixed rather than read
// from the terminal, since the output lands in CI logs as often as on screens.
const defaultWidth = 78

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// renderDiagnostics writes every problem found, where hcl.Diagnostics.Error
// summarises the rest as a count, so a file can be fixed in one pass.
func renderDiagnostics(ui cli.Ui, files map[string]*hcl.File, diags hcl.Diagnostics, color bool) {
	positioned, plain := splitByPosition(diags)

	for _, text := range renderPositioned(files, positioned, color) {
		ui.Error(text)
	}

	for _, d := range plain {
		ui.Error(formatDiagnostic(d))
	}
}

// splitByPosition separates the diagnostics with a source range, which hcl's
// writer renders, from those without.
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

// renderPositioned runs hcl's writer over each ranged diagnostic separately, so
// the UI receives one message per diagnostic and a prefixing UI can mark each.
func renderPositioned(files map[string]*hcl.File, diags hcl.Diagnostics, color bool) []string {
	if len(diags) == 0 {
		return nil
	}

	out := make([]string, 0, len(diags))

	for _, d := range diags {
		var buf bytes.Buffer

		writer := hcl.NewDiagnosticTextWriter(&buf, files, defaultWidth, color)
		if err := writer.WriteDiagnostic(d); err != nil {
			// A buffer cannot fail; fall back rather than lose the diagnostic
			out = append(out, formatDiagnostic(d))

			continue
		}

		out = append(out, strings.TrimRight(buf.String(), "\n"))
	}

	return out
}

// formatDiagnostic renders one diagnostic without a source excerpt, for
// validation rules, whose messages name the job and task instead.
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

// useColor reports whether output to w should carry escape sequences: never
// with NO_COLOR set, and otherwise only to a terminal.
func useColor(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}

	return isTerminal(w)
}

// isTerminal reports whether w is a character device. Stdlib rather than a
// library, to keep a dependency out of a tool that runs other people's code.
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
