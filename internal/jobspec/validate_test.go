// -------------------------------------------------------------------------------
// Semantic Validation Tests
//
// Author: Alex Freidah
//
// Each rule gets a job that breaks it and an assertion on what the diagnostic
// says. The messages are the product here: a rule that fires with a message an
// author cannot act on has not helped them.
// -------------------------------------------------------------------------------

package jobspec

import (
	"strings"
	"testing"
)

// validate parses a snippet and runs the rules over it, returning what they
// found. Parsing itself must succeed: a rule cannot run against a file that
// never decoded.
func validate(t *testing.T, src string, meta map[string]string) []string {
	t.Helper()

	file, diags := Parse(Config{
		Filename: "test.vagabond.hcl",
		Source:   []byte(src),
		Meta:     meta,
	})

	if diags.HasErrors() {
		t.Fatalf("parsing failed before validation could run: %s", diags.Error())
	}

	found := Validate(file)

	messages := make([]string, 0, len(found))
	for _, d := range found {
		messages = append(messages, d.Summary+": "+d.Detail)
	}

	return messages
}

// mentions reports whether any diagnostic contains every fragment given, which
// is how a test states what a message has to tell an author.
func mentions(messages []string, fragments ...string) bool {
	for _, msg := range messages {
		matched := true

		for _, fragment := range fragments {
			if !strings.Contains(msg, fragment) {
				matched = false

				break
			}
		}

		if matched {
			return true
		}
	}

	return false
}

// -------------------------------------------------------------------------
// THE VALID CASE
// -------------------------------------------------------------------------

// The fixture uses every block the specification defines, so it is the strongest
// statement that the rules do not fire on a correct job.
func TestValidate_FixtureIsClean(t *testing.T) {
	file, diags := ParseFile(fixturePath, map[string]string{"git_ref": "abc123"})
	if diags.HasErrors() {
		t.Fatalf("parsing the fixture failed: %s", diags.Error())
	}

	if found := Validate(file); found.HasErrors() {
		t.Errorf("the fixture does not validate: %s", found.Error())
	}
}

func TestValidate_MinimalJobIsClean(t *testing.T) {
	if messages := validate(t, minimalJob, nil); len(messages) != 0 {
		t.Errorf("a minimal job produced diagnostics: %v", messages)
	}
}

// -------------------------------------------------------------------------
// CLOSED VOCABULARIES
// -------------------------------------------------------------------------

// This is the gap TestParse_DoesNotEnforceVocabularies pins open at the parser.
// Closing it here is the whole reason the Drivers, Types, Arches, and
// Strategies accessors exist.
func TestValidate_ClosedVocabularies(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		fragments []string
	}{
		{
			name:      "retired oci-job driver",
			src:       strings.Replace(minimalJob, `"container"`, `"oci-job"`, 1),
			fragments: []string{"oci-job", "container, function, worker"},
		},
		{
			name:      "nomad service job type",
			src:       strings.Replace(minimalJob, `type = "batch"`, `type = "service"`, 1),
			fragments: []string{"service", "batch"},
		},
		{
			name: "unknown architecture",
			src: strings.Replace(minimalJob, `driver = "container"`,
				`driver = "container"

    execution {
      architecture = "x86_64"
    }`, 1),
			fragments: []string{"x86_64", "amd64, arm64"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := validate(t, tt.src, nil)

			if !mentions(messages, tt.fragments...) {
				t.Errorf("diagnostics do not name both the value and the valid set: %v", messages)
			}
		})
	}
}

// -------------------------------------------------------------------------
// STRUCTURE
// -------------------------------------------------------------------------

func TestValidate_JobWithNoTasks(t *testing.T) {
	messages := validate(t, `
job "empty" {
  type = "batch"
}
`, nil)

	if !mentions(messages, "No tasks", "empty") {
		t.Errorf("diagnostics do not report the missing task: %v", messages)
	}
}

// Task names identify a task in results and logs, so two tasks sharing one
// would make an execution unattributable.
func TestValidate_DuplicateTaskNames(t *testing.T) {
	messages := validate(t, `
job "twice" {
  type = "batch"

  task "build" {
    driver = "container"

    config {
      image = "alpine:latest"
    }
  }

  task "build" {
    driver = "container"

    config {
      image = "alpine:latest"
    }
  }
}
`, nil)

	if !mentions(messages, "Duplicate task name", "build") {
		t.Errorf("diagnostics do not report the duplicate: %v", messages)
	}
}

// The specification holds a slice of jobs so that a second one is reported
// rather than silently discarded by a parser that took the first and stopped.
func TestValidate_MultipleJobsInOneFile(t *testing.T) {
	src := minimalJob + `
job "second" {
  type = "batch"

  task "verify" {
    driver = "container"

    config {
      image = "alpine:latest"
    }
  }
}
`

	messages := validate(t, src, nil)

	if !mentions(messages, "Too many jobs") {
		t.Errorf("diagnostics do not report the second job: %v", messages)
	}
}

// -------------------------------------------------------------------------
// TASK CONFIGURATION
// -------------------------------------------------------------------------

// A container task runs an image. Anything more specific about the config block
// belongs to the driver, which is the only thing that knows its schema.
func TestValidate_ContainerWithoutImage(t *testing.T) {
	messages := validate(t, `
job "build" {
  type = "batch"

  task "compile" {
    driver = "container"

    config {
      command = "make"
    }
  }
}
`, nil)

	if !mentions(messages, "Missing image", "compile") {
		t.Errorf("diagnostics do not report the missing image: %v", messages)
	}
}

func TestValidate_ContainerWithNoConfigAtAll(t *testing.T) {
	messages := validate(t, `
job "build" {
  type = "batch"

  task "compile" {
    driver = "container"
  }
}
`, nil)

	if !mentions(messages, "Missing image") {
		t.Errorf("diagnostics do not report the missing config: %v", messages)
	}
}

// -------------------------------------------------------------------------
// DURATIONS
// -------------------------------------------------------------------------

// A Duration holds the text a job file carried, because gohcl cannot convert a
// string into a parsed one. This is where that text is proven.
func TestValidate_UnparseableTimeout(t *testing.T) {
	src := strings.Replace(minimalJob, `driver = "container"`,
		`driver  = "container"
    timeout = "soon"`, 1)

	messages := validate(t, src, nil)

	if !mentions(messages, "Invalid timeout", "soon") {
		t.Errorf("diagnostics do not report the unparseable timeout: %v", messages)
	}
}

// Zero bounds nothing, and an omitted attribute already means unbounded, so the
// two spellings should not differ.
func TestValidate_ZeroTimeout(t *testing.T) {
	src := strings.Replace(minimalJob, `driver = "container"`,
		`driver  = "container"
    timeout = "0s"`, 1)

	messages := validate(t, src, nil)

	if !mentions(messages, "Invalid timeout", "bounds nothing") {
		t.Errorf("diagnostics do not report the zero timeout: %v", messages)
	}
}

// -------------------------------------------------------------------------
// RESOURCES
// -------------------------------------------------------------------------

func TestValidate_NonPositiveResources(t *testing.T) {
	tests := []struct {
		name      string
		resources string
		fragment  string
	}{
		{name: "zero cpu", resources: "cpu = 0", fragment: "Invalid CPU request"},
		{name: "negative cpu", resources: "cpu = -1", fragment: "Invalid CPU request"},
		{name: "zero memory", resources: "memory = 0", fragment: "Invalid memory request"},
		{name: "negative memory", resources: "memory = -8", fragment: "Invalid memory request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := strings.Replace(minimalJob, `driver = "container"`,
				`driver = "container"

    resources {
      `+tt.resources+`
    }`, 1)

			messages := validate(t, src, nil)

			if !mentions(messages, tt.fragment) {
				t.Errorf("diagnostics do not report the resource: %v", messages)
			}
		})
	}
}

// -------------------------------------------------------------------------
// RETRY
// -------------------------------------------------------------------------

// A maximum below the initial delay is never reached, so the policy does not
// mean what it says.
func TestValidate_BackoffMaxBelowInitial(t *testing.T) {
	src := strings.Replace(minimalJob, `driver = "container"`,
		`driver = "container"

    retry {
      attempts = 2

      backoff {
        initial = "30s"
        max     = "5s"
      }
    }`, 1)

	messages := validate(t, src, nil)

	if !mentions(messages, "Invalid backoff", "never reached") {
		t.Errorf("diagnostics do not report the inverted backoff: %v", messages)
	}
}

func TestValidate_NegativeRetryAttempts(t *testing.T) {
	src := strings.Replace(minimalJob, `driver = "container"`,
		`driver = "container"

    retry {
      attempts = -1
    }`, 1)

	messages := validate(t, src, nil)

	if !mentions(messages, "Invalid retry attempts") {
		t.Errorf("diagnostics do not report the negative attempts: %v", messages)
	}
}

// Zero attempts is legitimate: it forbids retrying, which is a real policy.
func TestValidate_ZeroRetryAttemptsIsAllowed(t *testing.T) {
	src := strings.Replace(minimalJob, `driver = "container"`,
		`driver = "container"

    retry {
      attempts = 0
    }`, 1)

	if messages := validate(t, src, nil); len(messages) != 0 {
		t.Errorf("forbidding retries produced diagnostics: %v", messages)
	}
}

// -------------------------------------------------------------------------
// ROUTING
// -------------------------------------------------------------------------

// An attribute outside the reserved prefix matches nothing, so a job
// constraining on it would silently exclude every provider.
func TestValidate_UnknownConstraintAttribute(t *testing.T) {
	src := strings.Replace(minimalJob, `type = "batch"`,
		`type = "batch"

  routing {
    constraint {
      attribute = "node.class"
      operator  = "="
      value     = "large"
    }
  }`, 1)

	messages := validate(t, src, nil)

	if !mentions(messages, "Unknown constraint attribute", "node.class", "provider.") {
		t.Errorf("diagnostics do not report the unknown attribute: %v", messages)
	}
}

func TestValidate_UnknownStrategy(t *testing.T) {
	src := strings.Replace(minimalJob, `type = "batch"`,
		`type = "batch"

  routing {
    strategy = "cheapest"
  }`, 1)

	messages := validate(t, src, nil)

	if !mentions(messages, "Unknown routing strategy", "cheapest", "free-first") {
		t.Errorf("diagnostics do not report the unknown strategy: %v", messages)
	}
}

// An affinity that cannot raise a score does nothing, which is more likely a
// mistake than an intention.
func TestValidate_NonPositiveAffinityWeight(t *testing.T) {
	src := strings.Replace(minimalJob, `type = "batch"`,
		`type = "batch"

  routing {
    affinity {
      attribute = "provider.free_quota_percent"
      operator  = ">"
      value     = "50"
      weight    = 0
    }
  }`, 1)

	messages := validate(t, src, nil)

	if !mentions(messages, "Invalid affinity weight") {
		t.Errorf("diagnostics do not report the weight: %v", messages)
	}
}

// -------------------------------------------------------------------------
// COLLECTING
// -------------------------------------------------------------------------

// A validator that stops at the first problem makes an author fix a file one
// line per run.
func TestValidate_ReportsEveryProblem(t *testing.T) {
	messages := validate(t, `
job "broken" {
  type = "service"

  routing {
    strategy = "cheapest"
  }

  task "one" {
    driver  = "oci-job"
    timeout = "0s"
  }
}
`, nil)

	if len(messages) < 4 {
		t.Errorf("got %d diagnostics, want at least 4:\n%s",
			len(messages), strings.Join(messages, "\n"))
	}
}

// Validate is safe to call on nothing, so a caller need not check first.
func TestValidate_NilFile(t *testing.T) {
	if diags := Validate(nil); diags.HasErrors() {
		t.Errorf("validating nil produced diagnostics: %s", diags.Error())
	}
}

// A file that parsed to no jobs at all is reported rather than passing quietly.
func TestValidate_EmptyFile(t *testing.T) {
	file, diags := Parse(Config{Filename: "test.vagabond.hcl", Source: []byte("")})
	if diags.HasErrors() {
		t.Fatalf("parsing an empty file failed: %s", diags.Error())
	}

	if found := Validate(file); !found.HasErrors() {
		t.Error("an empty specification validated cleanly")
	}
}
