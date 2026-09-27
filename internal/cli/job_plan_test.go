// -------------------------------------------------------------------------------
// job plan Tests
//
// Author: Alex Freidah
//
// Exercised through Run against a job file and a server over the fixture
// configuration, so what is asserted is what an operator would read.
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

// With no store the ledger starts empty, so both container providers report
// full headroom and rank in name order.
const planConfig = `
provider "ibm-code-engine" {
  type = "fake-container"
}

provider "gcp-cloud-run" {
  type = "fake-container"
}

provider "aws-lambda" {
  type = "fake-function"
}
`

// plan runs the command with a job file against a server over cfg.
func plan(t *testing.T, job, cfg string, extra ...string) (int, string, string) {
	t.Helper()

	return serve(t, cfg).at([]string{"job", "plan"}, append(extra, writeJob(t, job))...)
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
		"Selected: gcp-cloud-run",
		"Estimated cost: free",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan output is missing %q:\n%s", want, stdout)
		}
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
		if before, _, ok := strings.Cut(line, "observed "); ok {
			lines[i] = before
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
//
// Spent here means a pool smaller than one run of the task, since an empty
// ledger is all config can express.
func TestJobPlan_TransientRejectionSaysSo(t *testing.T) {
	const timed = `
job "ci" {
  type = "batch"

  routing {
    providers = ["ibm-code-engine"]
  }

  task "test" {
    driver  = "container"
    timeout = "1h"

    config {
      image = "golang:1.27"
    }
  }
}
`

	const spent = `
provider "ibm-code-engine" {
  type = "fake-container"

  pool "runtime" {
    meter  = "seconds"
    limit  = 1
    period = "monthly"
  }
}
`

	code, stdout, _ := plan(t, timed, spent)

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
}

provider "cloudflare-workers" {
  type = "fake-worker"
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
// NAMESPACES
// -------------------------------------------------------------------------

// A one-hour task on a provider with room, planned from a namespace whose share
// of it is one second.
const (
	timedJob = `
job "ci" {
  type = "batch"

  task "test" {
    driver  = "container"
    timeout = "1h"

    config {
      image = "golang:1.27"
    }
  }
}
`

	sharedConfig = `
provider "ibm-code-engine" { type = "fake-container" }

namespace "ci" {
  quota "ibm-code-engine" {
    pool "runtime" {
      meter  = "seconds"
      limit  = 1
      period = "monthly"
    }
  }
}
`
)

// The namespace's share refuses the task even though the provider itself has
// room, and says whose pool it was.
func TestJobPlan_NamespaceShareRefuses(t *testing.T) {
	code, stdout, _ := plan(t, timedJob, sharedConfig, "-namespace", "ci")

	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	if !strings.Contains(stdout, `Namespace "ci"'s pool "runtime"`) {
		t.Errorf("the refusal does not name the namespace's pool:\n%s", stdout)
	}
}

// The same job in the default namespace has no share, so only the provider's
// total applies.
func TestJobPlan_DefaultNamespaceHasNoShare(t *testing.T) {
	code, stdout, _ := plan(t, timedJob, sharedConfig)

	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d\n%s", code, ExitSuccess, stdout)
	}
}

func TestJobPlan_UndeclaredNamespace(t *testing.T) {
	code, _, stderr := plan(t, planJob, planConfig, "-namespace", "nowhere")

	if code != ExitFailure || !strings.Contains(stderr, `namespace "nowhere" is not declared`) {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

// A job naming its namespace and a flag naming another is an error rather than
// one silently winning.
func TestJobPlan_NamespaceConflict(t *testing.T) {
	const pinned = `
job "ci" {
  type      = "batch"
  namespace = "ci"

  task "test" {
    driver = "container"

    config {
      image = "golang:1.27"
    }
  }
}
`

	code, _, stderr := plan(t, pinned, sharedConfig, "-namespace", "default")

	if code != ExitFailure || !strings.Contains(stderr, "remove one") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

// -------------------------------------------------------------------------
// THE SERVER
// -------------------------------------------------------------------------

// The address comes from VAGABOND_ADDR when the flag is not given.
func TestJobPlan_AddressFromEnvironment(t *testing.T) {
	s := serve(t, planConfig)
	t.Setenv(addressEnv, s.address)

	code, stdout, stderr := run("job", "plan", writeJob(t, planJob))

	if code != ExitSuccess || !strings.Contains(stdout, "Selected: gcp-cloud-run") {
		t.Errorf("exit %d, the plan did not reach the server:\n%s%s", code, stdout, stderr)
	}
}

// No server at the address is an error that names where it looked.
func TestJobPlan_NoServer(t *testing.T) {
	t.Setenv(addressEnv, "")

	code, _, stderr := run("job", "plan", "-address", "127.0.0.1:1", writeJob(t, planJob))

	if code != ExitFailure || !strings.Contains(stderr, "http://127.0.0.1:1") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

// A registered job is planned by name.
func TestJobPlan_RegisteredJob(t *testing.T) {
	s := serve(t, planConfig)

	if code, _, stderr := s.at([]string{"job", "register"}, writeJob(t, planJob)); code != ExitSuccess {
		t.Fatalf("register: exit %d\n%s", code, stderr)
	}

	code, stdout, stderr := s.at([]string{"job", "plan"}, "ci")

	if code != ExitSuccess || !strings.Contains(stdout, "Selected: gcp-cloud-run") {
		t.Errorf("exit %d:\n%s%s", code, stdout, stderr)
	}
}

// A name shaped like a file is read as one, so a typo reads as a missing file
// rather than a missing job.
func TestJobPlan_MissingFileIsAFile(t *testing.T) {
	code, _, stderr := serve(t, planConfig).at([]string{"job", "plan"}, "missing.hcl")

	if code != ExitFailure || !strings.Contains(stderr, "reading missing.hcl") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
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
	s := serve(t, planConfig)

	var out, errOut bytes.Buffer

	args := []string{"job", "plan", "-address", s.address, "-"}

	code := Run(args, strings.NewReader(planJob), &out, &errOut)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d\n%s%s", code, ExitSuccess, out.String(), errOut.String())
	}

	if !strings.Contains(out.String(), "Selected: gcp-cloud-run") {
		t.Errorf("a piped specification was not planned:\n%s", out.String())
	}
}

// -------------------------------------------------------------------------
// ARGUMENTS
// -------------------------------------------------------------------------

func TestJobPlan_WrongArgumentCount(t *testing.T) {
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
	examples := filepath.Join("..", "..", "examples")

	cfg, err := os.ReadFile(filepath.Join(examples, "config.hcl"))
	if err != nil {
		t.Fatalf("reading the example configuration: %v", err)
	}

	code, stdout, stderr := serve(t, string(cfg)).at([]string{"job", "plan"},
		"-meta", "version=1.2.3", filepath.Join(examples, "go-test.vagabond.hcl"))

	if code != ExitSuccess {
		t.Fatalf("the shipped examples do not plan: exit %d\n%s%s", code, stdout, stderr)
	}

	if !strings.Contains(stdout, "Selected: ") {
		t.Errorf("the example plan selected nothing:\n%s", stdout)
	}
}
