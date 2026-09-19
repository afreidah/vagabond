// -------------------------------------------------------------------------------
// job plan Tests
//
// Author: Alex Freidah
//
// Exercised through Run against real files, because the whole claim of this
// command is that a job file and a configuration file produce a plan with
// nothing else standing up. A test that reached past Run would not be testing
// that claim.
//
// Nothing here has credentials and nothing contacts anything, which is the
// property the chunk exists to demonstrate rather than a convenience.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planJob routes to two of the three providers below and constrains on an
// attribute only the container ones publish.
const planJob = `
job "ci" {
  type = "batch"

  routing {
    providers = ["ibm-code-engine", "gcp-cloud-run"]

    constraint {
      attribute = "provider.architecture"
      operator  = "set_contains"
      value     = "amd64"
    }

    affinity {
      attribute = "provider.free_quota_percent"
      operator  = ">"
      value     = "50"
      weight    = 75
    }
  }

  task "test" {
    driver = "container"

    config {
      image = "golang:1.27"
    }
  }
}
`

const planConfig = `
provider "ibm-code-engine" {
  type = "fake-container"
  quota { free_percent = 80 }
}

provider "gcp-cloud-run" {
  type = "fake-container"
  quota { free_percent = 45 }
}

provider "aws-lambda" {
  type = "fake-function"
  quota { free_percent = 90 }
}
`

// writeConfig puts provider configuration in a temporary file.
func writeConfig(t *testing.T, src string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "vagabond.hcl")

	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("writing the configuration: %v", err)
	}

	return path
}

// plan runs the command with a job and a configuration, both temporary.
func plan(t *testing.T, job, cfg string, extra ...string) (int, string, string) {
	t.Helper()

	// Nothing may be discovered from the environment or the working directory,
	// or a developer's own config would change what these tests assert.
	t.Setenv("VAGABOND_CONFIG", "")
	t.Chdir(t.TempDir())

	args := append([]string{"job", "plan", "-config", writeConfig(t, cfg)}, extra...)

	return run(append(args, writeJob(t, job))...)
}

// -------------------------------------------------------------------------
// THE HAPPY PATH
// -------------------------------------------------------------------------

func TestJobPlan_RendersTheTable(t *testing.T) {
	code, stdout, stderr := plan(t, planJob, planConfig)

	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d\n%s%s", code, ExitSuccess, stdout, stderr)
	}

	for _, want := range []string{
		"ci.test (container)",
		"ibm-code-engine",
		"admitted",
		"gcp-cloud-run",
		"aws-lambda",
		"rejected",
		"not-allowlisted",
		"Selected: ibm-code-engine",
		"Estimated cost: free",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan output is missing %q:\n%s", want, stdout)
		}
	}
}

// The provider with more headroom wins, and the affinity the job wrote is what
// separates them. Both are admitted, so ordering is the only visible effect.
func TestJobPlan_RanksByHeadroom(t *testing.T) {
	_, stdout, _ := plan(t, planJob, planConfig)

	ibm := strings.Index(stdout, "ibm-code-engine")
	gcp := strings.Index(stdout, "gcp-cloud-run")

	if ibm < 0 || gcp < 0 {
		t.Fatalf("both providers should appear:\n%s", stdout)
	}

	if ibm > gcp {
		t.Errorf("the fuller provider ranked second:\n%s", stdout)
	}
}

// Plan output is diffed in CI, so the same inputs have to produce the same
// bytes. The observation timestamps come from a refresh, so this runs one plan
// and compares the lines that are not time.
func TestJobPlan_IsRepeatable(t *testing.T) {
	_, first, _ := plan(t, planJob, planConfig)
	_, second, _ := plan(t, planJob, planConfig)

	if stripObserved(first) != stripObserved(second) {
		t.Errorf("plan output differs between runs:\n%s\n---\n%s", first, second)
	}
}

// stripObserved removes the snapshot timestamps, which are the only part of a
// plan that legitimately changes between runs.
func stripObserved(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "observed "); idx >= 0 {
			lines[i] = line[:idx]
		}
	}

	return strings.Join(lines, "\n")
}

func TestJobPlan_Verbose(t *testing.T) {
	_, stdout, _ := plan(t, planJob, planConfig, "-verbose")

	// The score breakdown is what defends the number rather than asking to be
	// trusted on it.
	for _, want := range []string{"headroom", "affinity"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("verbose output is missing the %s scorer:\n%s", want, stdout)
		}
	}

	// And a rejection shows every reason, not only the one that printed.
	if !strings.Contains(stdout, "driver-unsupported") {
		t.Errorf("verbose output omits the collected reasons:\n%s", stdout)
	}
}

// -------------------------------------------------------------------------
// NOWHERE TO RUN
// -------------------------------------------------------------------------

// A job nothing can run exits non-zero, because that is what a CI system gates
// on, and names every provider with the reason it was refused.
func TestJobPlan_NothingAdmitted(t *testing.T) {
	const impossible = `
job "ci" {
  type = "batch"

  task "test" {
    driver = "container"

    config {
      image = "golang:1.27"
    }
  }
}
`

	const functionsOnly = `
provider "aws-lambda" {
  type = "fake-function"
  quota { free_percent = 90 }
}
`

	code, stdout, _ := plan(t, impossible, functionsOnly)

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d\n%s", code, ExitFailure, stdout)
	}

	for _, want := range []string{
		"aws-lambda",
		"driver-unsupported",
		"No provider can run this task.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan output is missing %q:\n%s", want, stdout)
		}
	}
}

// A rejection that may pass on its own says so, which is the difference
// between waiting and editing.
func TestJobPlan_TransientRejectionSaysSo(t *testing.T) {
	const spent = `
provider "ibm-code-engine" {
  type = "fake-container"
  quota { exhausted = true }
}
`

	code, stdout, _ := plan(t, planJob, spent)

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if !strings.Contains(stdout, "may be admitted later") {
		t.Errorf("a transient rejection did not say so:\n%s", stdout)
	}
}

// Reasons differ where the causes differ, rather than collapsing into one
// unhelpful code for everything.
func TestJobPlan_ReasonsDifferByCause(t *testing.T) {
	const mixed = `
provider "ibm-code-engine" {
  type    = "fake-container"
  enabled = false
}

provider "aws-lambda" {
  type = "fake-function"
  quota { free_percent = 90 }
}

provider "cloudflare-workers" {
  type = "fake-worker"
  quota { exhausted = true }
}
`

	const anywhere = `
job "ci" {
  type = "batch"

  task "test" {
    driver = "container"

    config {
      image = "golang:1.27"
    }
  }
}
`

	_, stdout, _ := plan(t, anywhere, mixed)

	for _, want := range []string{"provider-disabled", "driver-unsupported"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan output is missing %q:\n%s", want, stdout)
		}
	}
}

// -------------------------------------------------------------------------
// CONFIGURATION
// -------------------------------------------------------------------------

// A plan against providers nobody configured would be a demonstration wearing
// a plan's output. Terraform does not invent providers either.
func TestJobPlan_NoConfiguration(t *testing.T) {
	t.Setenv("VAGABOND_CONFIG", "")
	t.Chdir(t.TempDir())

	code, _, stderr := run("job", "plan", writeJob(t, planJob))

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	// The error teaches the search path rather than leaving someone guessing.
	for _, want := range []string{"no configuration found", "VAGABOND_CONFIG", "vagabond.hcl"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("error is missing %q:\n%s", want, stderr)
		}
	}
}

func TestJobPlan_ConfigFromEnvironment(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("VAGABOND_CONFIG", writeConfig(t, planConfig))

	code, stdout, stderr := run("job", "plan", writeJob(t, planJob))

	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d\n%s%s", code, ExitSuccess, stdout, stderr)
	}

	if !strings.Contains(stdout, "Selected: ibm-code-engine") {
		t.Errorf("plan did not use the configuration from the environment:\n%s", stdout)
	}
}

// A configuration that parses and registers nothing is a different thing from
// no configuration, and the two must not read alike.
func TestJobPlan_ConfigWithNoProviders(t *testing.T) {
	code, stdout, stderr := plan(t, planJob, "# nothing here\n")

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d\n%s", code, ExitFailure, stdout)
	}

	if !strings.Contains(stderr, "No providers are configured") {
		t.Errorf("unexpected error:\n%s", stderr)
	}
}

func TestJobPlan_BadConfiguration(t *testing.T) {
	code, _, stderr := plan(t, planJob, `provider "broken" { type = }`)

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if stderr == "" {
		t.Error("a malformed configuration reported nothing")
	}
}

// -------------------------------------------------------------------------
// THE JOB FILE
// -------------------------------------------------------------------------

// The same mistake has to read the same way in plan as in validate, so plan
// runs the rules rather than only the parser.
func TestJobPlan_InvalidJob(t *testing.T) {
	code, _, stderr := plan(t, `job "ci" { type = "nonsense" }`, planConfig)

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if stderr == "" {
		t.Error("an invalid job reported nothing")
	}
}

func TestJobPlan_MissingRequiredMeta(t *testing.T) {
	const parameterized = `
job "ci" {
  type = "batch"

  parameterized {
    meta_required = ["version"]
  }

  task "test" {
    driver = "container"

    config {
      image = "golang:${meta.version}"
    }
  }
}
`

	code, _, stderr := plan(t, parameterized, planConfig)

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if !strings.Contains(stderr, "version") {
		t.Errorf("error does not name the missing metadata:\n%s", stderr)
	}
}

// Metadata reaches the image the same way it would at submission, so a plan
// describes the job that would actually run.
func TestJobPlan_MetaIsSubstituted(t *testing.T) {
	const parameterized = `
job "ci" {
  type = "batch"

  parameterized {
    meta_required = ["version"]
  }

  task "test" {
    driver = "container"

    config {
      image = "golang:${meta.version}"
    }
  }
}
`

	code, stdout, stderr := plan(t, parameterized, planConfig, "-meta", "version=1.27")

	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d\n%s%s", code, ExitSuccess, stdout, stderr)
	}

	if !strings.Contains(stdout, "admitted") {
		t.Errorf("a parameterized job was not planned:\n%s", stdout)
	}
}

func TestJobPlan_ReadsStdin(t *testing.T) {
	t.Setenv("VAGABOND_CONFIG", "")
	t.Chdir(t.TempDir())

	var out, errOut bytes.Buffer

	args := []string{"job", "plan", "-config", writeConfig(t, planConfig), "-"}

	code := Run(args, strings.NewReader(planJob), &out, &errOut)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d\n%s%s", code, ExitSuccess, out.String(), errOut.String())
	}

	if !strings.Contains(out.String(), "Selected: ibm-code-engine") {
		t.Errorf("a piped specification was not planned:\n%s", out.String())
	}
}

// -------------------------------------------------------------------------
// ARGUMENTS
// -------------------------------------------------------------------------

func TestJobPlan_WrongArgumentCount(t *testing.T) {
	t.Setenv("VAGABOND_CONFIG", "")

	for _, args := range [][]string{
		{"job", "plan"},
		{"job", "plan", "one.hcl", "two.hcl"},
	} {
		code, _, stderr := run(args...)

		if code != ExitFailure {
			t.Errorf("%v: exit code = %d, want %d", args, code, ExitFailure)
		}

		if !strings.Contains(stderr, "one argument") {
			t.Errorf("%v: unexpected error:\n%s", args, stderr)
		}
	}
}

// -------------------------------------------------------------------------
// THE DOCUMENTED EXAMPLE
// -------------------------------------------------------------------------

// The example job and the example configuration have to work together. Two
// examples that each parse and cannot be used with each other are worse than
// one, because the first thing anyone does is run them side by side.
func TestJobPlan_ShippedExamples(t *testing.T) {
	t.Setenv("VAGABOND_CONFIG", "")

	examples := filepath.Join("..", "..", "examples")

	code, stdout, stderr := run(
		"job", "plan",
		"-config", filepath.Join(examples, "config.hcl"),
		"-meta", "version=1.2.3",
		filepath.Join(examples, "go-test.vagabond.hcl"),
	)

	if code != ExitSuccess {
		t.Fatalf("the shipped examples do not plan: exit %d\n%s%s", code, stdout, stderr)
	}

	if !strings.Contains(stdout, "Selected: ") {
		t.Errorf("the example plan selected nothing:\n%s", stdout)
	}
}
