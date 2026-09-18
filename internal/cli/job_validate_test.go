// -------------------------------------------------------------------------------
// job validate Tests
//
// Author: Alex Freidah
//
// Exercised through Run against a real file in a temporary directory, which is
// the only way to prove the command reads, parses, validates, and reports. The
// packages underneath have their own tests; these cover the wiring.
// -------------------------------------------------------------------------------

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
)

const validJob = `
job "build" {
  type = "batch"

  task "compile" {
    driver = "container"

    config {
      image = "alpine:latest"
    }
  }
}
`

// writeJob puts a specification in a temporary file and returns its path.
func writeJob(t *testing.T, src string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.vagabond.hcl")

	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("writing the job file: %v", err)
	}

	return path
}

// -------------------------------------------------------------------------
// THE HAPPY PATH
// -------------------------------------------------------------------------

func TestJobValidate_ValidFile(t *testing.T) {
	code, stdout, stderr := run("job", "validate", writeJob(t, validJob))

	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d\n%s", code, ExitSuccess, stderr)
	}

	if !strings.Contains(stdout, "is valid") {
		t.Errorf("stdout does not confirm validity:\n%s", stdout)
	}
}

func TestJobValidate_MissingFile(t *testing.T) {
	code, _, stderr := run("job", "validate", filepath.Join(t.TempDir(), "absent.hcl"))

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if !strings.Contains(stderr, "Cannot read job file") {
		t.Errorf("stderr does not explain the read failure:\n%s", stderr)
	}
}

// -------------------------------------------------------------------------
// FAILURES
// -------------------------------------------------------------------------

func TestJobValidate_SyntaxError(t *testing.T) {
	code, _, stderr := run("job", "validate", writeJob(t, `job "broken" {`))

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if stderr == "" {
		t.Error("a syntax error produced no output")
	}
}

// A validation failure has to be distinguishable from a syntax error, because
// they call for different fixes.
func TestJobValidate_ValidationError(t *testing.T) {
	src := strings.Replace(validJob, `"container"`, `"oci-job"`, 1)

	code, _, stderr := run("job", "validate", writeJob(t, src))

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if !strings.Contains(stderr, "Unknown driver") {
		t.Errorf("stderr does not report the unknown driver:\n%s", stderr)
	}

	if !strings.Contains(stderr, "container, function, worker") {
		t.Errorf("stderr does not list the valid drivers:\n%s", stderr)
	}
}

// Every problem is reported, not just the first. Fixing a file one line per run
// is the thing a validator exists to avoid.
func TestJobValidate_ReportsEveryProblem(t *testing.T) {
	src := `
job "broken" {
  type = "service"

  task "one" {
    driver  = "oci-job"
    timeout = "0s"
  }
}
`

	_, _, stderr := run("job", "validate", writeJob(t, src))

	for _, want := range []string{"Unknown job type", "Unknown driver", "Invalid timeout"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr is missing %q:\n%s", want, stderr)
		}
	}
}

// -------------------------------------------------------------------------
// METADATA
// -------------------------------------------------------------------------

const parameterizedFile = `
job "build" {
  type = "batch"

  parameterized {
    meta_required = ["version"]
  }

  task "compile" {
    driver = "container"

    config {
      image = "alpine:latest"
    }

    source {
      type       = "git"
      repository = "https://git.example.com/example/app.git"
      ref        = "${meta.version}"
    }
  }
}
`

func TestJobValidate_MetaSupplied(t *testing.T) {
	path := writeJob(t, parameterizedFile)

	code, stdout, stderr := run("job", "validate", "-meta", "version=1.4.2", path)

	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d\n%s", code, ExitSuccess, stderr)
	}

	if !strings.Contains(stdout, "is valid") {
		t.Errorf("stdout does not confirm validity:\n%s", stdout)
	}
}

// Matching Nomad: a job referencing metadata nobody supplied is refused rather
// than validating and then interpolating nothing at submission.
func TestJobValidate_MetaMissing(t *testing.T) {
	code, _, stderr := run("job", "validate", writeJob(t, parameterizedFile))

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if !strings.Contains(stderr, "version") {
		t.Errorf("stderr does not name the missing key:\n%s", stderr)
	}

	if !strings.Contains(stderr, "-meta") {
		t.Errorf("stderr does not say how to supply it:\n%s", stderr)
	}
}

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// A diagnostic with a position shows it; one without must not print "<nil>".
func TestFormatDiagnostic(t *testing.T) {
	positioned := &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Something is wrong",
		Detail:   "Here is why.",
		Subject: &hcl.Range{
			Filename: "test.vagabond.hcl",
			Start:    hcl.Pos{Line: 4, Column: 2},
			End:      hcl.Pos{Line: 4, Column: 9},
		},
	}

	rendered := formatDiagnostic(positioned)

	if !strings.Contains(rendered, "test.vagabond.hcl") {
		t.Errorf("a positioned diagnostic omits its file:\n%s", rendered)
	}

	if !strings.Contains(rendered, "Here is why.") {
		t.Errorf("the detail was dropped:\n%s", rendered)
	}

	unpositioned := &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Something is wrong",
		Detail:   "Here is why.",
	}

	rendered = formatDiagnostic(unpositioned)

	if strings.Contains(rendered, "nil") {
		t.Errorf("an unpositioned diagnostic printed a nil range:\n%s", rendered)
	}
}

// A diagnostic with no detail renders its summary and nothing else.
func TestFormatDiagnostic_SummaryOnly(t *testing.T) {
	rendered := formatDiagnostic(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Terse",
	})

	if strings.TrimSpace(rendered) != "Terse" {
		t.Errorf("rendered = %q, want just the summary", rendered)
	}
}
