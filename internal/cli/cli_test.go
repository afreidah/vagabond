// -------------------------------------------------------------------------------
// CLI Tests
//
// Author: Alex Freidah
//
// Run takes its streams rather than reaching for os.Stdout precisely so these
// exist: the whole command surface is exercised in-process, with no binary
// built and no process spawned.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"strings"
	"testing"
)

// run executes the CLI against buffers and returns the exit code and what was
// written to each stream.
func run(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer

	code = Run(args, strings.NewReader(""), &out, &errOut)

	return code, out.String(), errOut.String()
}

// -------------------------------------------------------------------------
// EXIT CODES
// -------------------------------------------------------------------------

// The CLI documents two exit codes, and a caller branching on them must never
// see a third. The library returns 127 for an unmatched command, which at a
// shell means the binary itself is missing.
func TestRun_ExitCodesAreOnlyZeroOrOne(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no command", args: nil},
		{name: "unknown command", args: []string{"nonsense"}},
		{name: "unknown subcommand", args: []string{"job", "nonsense"}},
		{name: "namespace alone", args: []string{"job"}},
		{name: "missing argument", args: []string{"job", "validate"}},
		{name: "unknown flag", args: []string{"job", "validate", "-nope"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, _ := run(tt.args...)

			if code != ExitSuccess && code != ExitFailure {
				t.Errorf("exit code = %d, want %d or %d", code, ExitSuccess, ExitFailure)
			}
		})
	}
}

func TestRun_VersionSucceeds(t *testing.T) {
	code, stdout, _ := run("-version")

	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d", code, ExitSuccess)
	}

	if strings.TrimSpace(stdout) == "" {
		t.Error("-version printed nothing")
	}
}

// -------------------------------------------------------------------------
// HELP
// -------------------------------------------------------------------------

// The usage line uses single dashes, which is the convention this CLI was built
// on hashicorp/cli to preserve. The library's own help function hardcodes
// double dashes, so this asserts ours is in use.
func TestRun_HelpUsesSingleDashFlags(t *testing.T) {
	_, stdout, stderr := run()
	output := stdout + stderr

	if !strings.Contains(output, "[-version]") {
		t.Errorf("usage line does not offer -version:\n%s", output)
	}

	if strings.Contains(output, "--version") {
		t.Errorf("usage line offers --version, breaking the single-dash convention:\n%s", output)
	}
}

// A namespace with no description tells a reader nothing, which is why the
// namespace is registered as a real command rather than left synthesised.
func TestRun_HelpListsNamespaceWithSynopsis(t *testing.T) {
	_, stdout, stderr := run()
	output := stdout + stderr

	if !strings.Contains(output, "job") {
		t.Errorf("help does not list the job namespace:\n%s", output)
	}

	if !strings.Contains(output, "Interact with jobs") {
		t.Errorf("help lists the job namespace without a synopsis:\n%s", output)
	}
}

func TestRun_JobNamespaceListsSubcommands(t *testing.T) {
	_, stdout, stderr := run("job", "-help")
	output := stdout + stderr

	if !strings.Contains(output, "validate") {
		t.Errorf("job help does not mention validate:\n%s", output)
	}
}

// -------------------------------------------------------------------------
// ARGUMENTS
// -------------------------------------------------------------------------

// A command invoked wrongly must say what was expected, not just fail.
func TestJobValidate_MissingPathExplainsItself(t *testing.T) {
	code, stdout, stderr := run("job", "validate")
	output := stdout + stderr

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if !strings.Contains(output, "<path>") {
		t.Errorf("error does not name the missing argument:\n%s", output)
	}
}

func TestJobValidate_TooManyPaths(t *testing.T) {
	code, _, _ := run("job", "validate", "one.hcl", "two.hcl")

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}
}

// -------------------------------------------------------------------------
// THE -meta FLAG
// -------------------------------------------------------------------------

func TestMetaFlags_Set(t *testing.T) {
	var m metaFlags

	if err := m.Set("version=abc123"); err != nil {
		t.Fatalf("Set returned unexpected error: %v", err)
	}

	if got := m["version"]; got != "abc123" {
		t.Errorf("m[version] = %q, want abc123", got)
	}
}

// An empty value is legitimate: a caller may want to pass a key explicitly set
// to nothing rather than omit it.
func TestMetaFlags_EmptyValueIsAllowed(t *testing.T) {
	var m metaFlags

	if err := m.Set("key="); err != nil {
		t.Fatalf("Set returned unexpected error: %v", err)
	}

	if _, ok := m["key"]; !ok {
		t.Error("an explicitly empty value was dropped")
	}
}

func TestMetaFlags_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "no separator", input: "version"},
		{name: "empty key", input: "=abc123"},
		{name: "whitespace key", input: "   =abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m metaFlags

			if err := m.Set(tt.input); err == nil {
				t.Errorf("Set(%q) returned no error", tt.input)
			}
		})
	}
}

// A repeated key is a mistake upstream. Taking the last one quietly hides which
// value the submission actually ran with.
func TestMetaFlags_RepeatedKeyIsRejected(t *testing.T) {
	var m metaFlags

	if err := m.Set("version=abc"); err != nil {
		t.Fatalf("Set returned unexpected error: %v", err)
	}

	err := m.Set("version=def")
	if err == nil {
		t.Fatal("a repeated key was accepted")
	}

	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error does not name the repeated key: %v", err)
	}

	if m["version"] != "abc" {
		t.Errorf("the original value was overwritten: %q", m["version"])
	}
}

func TestMetaFlags_String(t *testing.T) {
	var empty metaFlags
	if empty.String() != "" {
		t.Errorf("empty metaFlags rendered as %q", empty.String())
	}

	m := metaFlags{"version": "abc"}
	if !strings.Contains(m.String(), "version=abc") {
		t.Errorf("String() = %q, want it to contain version=abc", m.String())
	}
}

// The flag reaches the command and is accepted, which is what makes a
// parameterized job submittable from the CLI at all.
func TestJobValidate_AcceptsMetaFlag(t *testing.T) {
	_, stdout, stderr := run("job", "validate", "-meta", "version=abc123", "some.hcl")
	output := stdout + stderr

	if strings.Contains(output, "flag provided but not defined") {
		t.Errorf("-meta was not accepted:\n%s", output)
	}
}
