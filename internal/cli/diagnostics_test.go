// -------------------------------------------------------------------------------
// Diagnostic Rendering Tests
//
// Author: Alex Freidah
//
// Rendered into a buffer rather than to a terminal, so these run in CI. The
// cases that matter are the two kinds of diagnostic mixed together: parse
// errors carry a range and get a source excerpt, validation errors carry none
// and must not print a position at all.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hashicorp/cli"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

const diagSource = `job "example" {
  type = "batch"
}
`

// sourceFiles parses a snippet into the map hcl's writer expects.
func sourceFiles(t *testing.T, name, src string) map[string]*hcl.File {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), name, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing: %s", diags.Error())
	}

	return map[string]*hcl.File{name: f}
}

// render collects what the renderer wrote to the error stream.
func render(t *testing.T, files map[string]*hcl.File, diags hcl.Diagnostics, color bool) string {
	t.Helper()

	var out, errOut bytes.Buffer

	ui := &cli.BasicUi{
		Reader:      strings.NewReader(""),
		Writer:      &out,
		ErrorWriter: &errOut,
	}

	renderDiagnostics(ui, files, diags, color)

	return errOut.String()
}

// -------------------------------------------------------------------------
// POSITIONED DIAGNOSTICS
// -------------------------------------------------------------------------

// A diagnostic with a range gets the offending line printed under it. That is
// the difference between a validator people run and one they stop running.
func TestRenderDiagnostics_ShowsSourceExcerpt(t *testing.T) {
	files := sourceFiles(t, "test.vagabond.hcl", diagSource)

	output := render(t, files, hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Something is wrong",
		Detail:   "Here is why.",
		Subject: &hcl.Range{
			Filename: "test.vagabond.hcl",
			Start:    hcl.Pos{Line: 2, Column: 3, Byte: 18},
			End:      hcl.Pos{Line: 2, Column: 7, Byte: 22},
		},
	}}, false)

	for _, want := range []string{"test.vagabond.hcl", "line 2", `type = "batch"`, "Here is why."} {
		if !strings.Contains(output, want) {
			t.Errorf("output is missing %q:\n%s", want, output)
		}
	}
}

// -------------------------------------------------------------------------
// UNPOSITIONED DIAGNOSTICS
// -------------------------------------------------------------------------

// Validation rules run against the decoded specification and carry no range, so
// they must render as plain text rather than with a position of "<nil>".
func TestRenderDiagnostics_PlainWhenUnpositioned(t *testing.T) {
	output := render(t, nil, hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  `Unknown driver in "build" task "compile"`,
		Detail:   "Valid drivers are container, function, worker.",
	}}, false)

	if strings.Contains(output, "nil") {
		t.Errorf("an unpositioned diagnostic printed a nil range:\n%s", output)
	}

	if !strings.Contains(output, "Unknown driver") {
		t.Errorf("the summary was dropped:\n%s", output)
	}

	if !strings.Contains(output, "container, function, worker") {
		t.Errorf("the detail was dropped:\n%s", output)
	}
}

// Both kinds arrive together from one run, and neither may swallow the other.
func TestRenderDiagnostics_MixedKinds(t *testing.T) {
	files := sourceFiles(t, "test.vagabond.hcl", diagSource)

	output := render(t, files, hcl.Diagnostics{
		{
			Severity: hcl.DiagError,
			Summary:  "Positioned problem",
			Subject: &hcl.Range{
				Filename: "test.vagabond.hcl",
				Start:    hcl.Pos{Line: 2, Column: 3, Byte: 18},
				End:      hcl.Pos{Line: 2, Column: 7, Byte: 22},
			},
		},
		{
			Severity: hcl.DiagError,
			Summary:  "Unpositioned problem",
		},
	}, false)

	for _, want := range []string{"Positioned problem", "Unpositioned problem"} {
		if !strings.Contains(output, want) {
			t.Errorf("output is missing %q:\n%s", want, output)
		}
	}
}

// Every problem is reported. hcl.Diagnostics.Error summarises as one and a
// count of the rest, which is the opposite of what a validator is for.
func TestRenderDiagnostics_ReportsEveryProblem(t *testing.T) {
	diags := hcl.Diagnostics{
		{Severity: hcl.DiagError, Summary: "First"},
		{Severity: hcl.DiagError, Summary: "Second"},
		{Severity: hcl.DiagError, Summary: "Third"},
	}

	output := render(t, nil, diags, false)

	for _, want := range []string{"First", "Second", "Third"} {
		if !strings.Contains(output, want) {
			t.Errorf("output is missing %q:\n%s", want, output)
		}
	}
}

// -------------------------------------------------------------------------
// COLOUR
// -------------------------------------------------------------------------

// Output that is not going to a terminal carries no escape sequences, which is
// noticed immediately by anyone reading a CI log.
func TestRenderDiagnostics_NoEscapesWithoutColor(t *testing.T) {
	files := sourceFiles(t, "test.vagabond.hcl", diagSource)

	output := render(t, files, hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Something is wrong",
		Subject: &hcl.Range{
			Filename: "test.vagabond.hcl",
			Start:    hcl.Pos{Line: 2, Column: 3, Byte: 18},
			End:      hcl.Pos{Line: 2, Column: 7, Byte: 22},
		},
	}}, false)

	if strings.Contains(output, "\x1b[") {
		t.Errorf("output carries escape sequences with colour disabled:\n%q", output)
	}
}

func TestUseColor_HonoursNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	if useColor(nil) {
		t.Error("NO_COLOR was ignored")
	}
}

// A buffer is not a terminal, which is what keeps test output and CI logs
// clean without anything having to ask.
func TestIsTerminal_BufferIsNot(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("a buffer reports as a terminal")
	}
}
