// -------------------------------------------------------------------------------
// Parsing Tests
//
// Author: Alex Freidah
//
// The acceptance test is at the bottom: the documented example has to decode
// into the same value the specification package builds by hand. Those literals
// were written in Chunk 1 to be compared against exactly this, which is what
// makes them worth the space they take.
// -------------------------------------------------------------------------------

package jobspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ptr"
)

// fixturePath is the job that uses every block the specification defines. It is
// a fixture rather than an example, so that a field can be added to cover it
// without changing what a reader is shown.
var fixturePath = filepath.Join("testdata", "complete.vagabond.hcl")

// examplesDir holds the files a reader is pointed at. Every one of them has to
// parse: an example that does not is worse than no example.
var examplesDir = filepath.Join("..", "..", "examples")

// parse is the common shape: parse a snippet and fail on any diagnostic.
func parse(t *testing.T, src string, meta map[string]string) *job.File {
	t.Helper()

	file, diags := Parse(Config{
		Filename: "test.vagabond.hcl",
		Source:   []byte(src),
		Meta:     meta,
	})

	if diags.HasErrors() {
		t.Fatalf("parsing failed: %s", diags.Error())
	}

	return file.Spec
}

// parseErr is the mirror: parse a snippet expected to fail, and hand back the
// diagnostics so a test can assert what they say.
func parseErr(t *testing.T, src string) hcl.Diagnostics {
	t.Helper()

	_, diags := Parse(Config{Filename: "test.vagabond.hcl", Source: []byte(src)})
	if !diags.HasErrors() {
		t.Fatal("expected parsing to fail")
	}

	return diags
}

const minimalJob = `
job "example" {
  type = "batch"

  task "verify" {
    driver = "container"

    config {
      image = "alpine:latest"
    }
  }
}
`

// -------------------------------------------------------------------------
// SHAPE
// -------------------------------------------------------------------------

func TestParse_MinimalJob(t *testing.T) {
	file := parse(t, minimalJob, nil)

	if len(file.Jobs) != 1 {
		t.Fatalf("len(Jobs) = %d, want 1", len(file.Jobs))
	}

	j := file.Jobs[0]

	if j.Name != "example" {
		t.Errorf("Name = %q, want example", j.Name)
	}

	if ptr.Deref(j.Type) != job.TypeBatch {
		t.Errorf("Type = %q, want %q", ptr.Deref(j.Type), job.TypeBatch)
	}

	if len(j.Tasks) != 1 || j.Tasks[0].Driver != job.DriverContainer {
		t.Errorf("task did not decode: %+v", j.Tasks)
	}
}

// An omitted optional attribute has to stay nil rather than becoming its zero
// value, which is the whole reason the specification uses pointers.
func TestParse_OmittedOptionalsStayNil(t *testing.T) {
	task := parse(t, minimalJob, nil).Jobs[0].Tasks[0]

	if task.Timeout != nil {
		t.Errorf("Timeout = %v, want nil for an omitted attribute", task.Timeout)
	}

	if task.Resources != nil {
		t.Errorf("Resources = %+v, want nil for an omitted block", task.Resources)
	}
}

// A file is a container, so a second job is decoded rather than discarded. That
// it is rejected at all is a validation rule, not a parsing one.
func TestParse_MultipleJobsAreDecoded(t *testing.T) {
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

	if got := len(parse(t, src, nil).Jobs); got != 2 {
		t.Errorf("len(Jobs) = %d, want 2", got)
	}
}

// -------------------------------------------------------------------------
// CLOSED VOCABULARIES
// -------------------------------------------------------------------------

// Decoding does not enforce the closed vocabularies, and this pins that so the
// gap is deliberate rather than discovered.
//
// gohcl decodes through gocty, which converts by reflected kind and consults
// neither encoding.TextUnmarshaler nor any custom decoder. A string-kinded type
// such as DriverName therefore accepts any string, and the UnmarshalText
// written in Chunk 1 is used by JSON and database columns but never by HCL.
//
// Enforcing the vocabulary is validation's job, where the diagnostic can name
// the valid set. Losing that test here would mean nobody notices when it is
// also missing there.
func TestParse_DoesNotEnforceVocabularies(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "retired oci-job driver",
			src:  strings.Replace(minimalJob, `"container"`, `"oci-job"`, 1),
			want: "oci-job",
		},
		{
			name: "nomad service job type",
			src:  strings.Replace(minimalJob, `"batch"`, `"service"`, 1),
			want: "service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, diags := Parse(Config{
				Filename: "test.vagabond.hcl",
				Source:   []byte(tt.src),
			})

			if diags.HasErrors() {
				t.Fatalf("decoding rejected the value, so validation may be unreachable: %s",
					diags.Error())
			}

			task := file.Spec.Jobs[0].Tasks[0]
			decoded := task.Driver.String() + " " + ptr.Deref(file.Spec.Jobs[0].Type).String()

			if !strings.Contains(decoded, tt.want) {
				t.Errorf("decoded %q, want it to carry %q", decoded, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// DIAGNOSTICS
// -------------------------------------------------------------------------

// Every diagnostic has to carry a position, or the renderer has nothing to
// point at and the validator is no better than "invalid job".
func TestParse_DiagnosticsCarrySourcePositions(t *testing.T) {
	diags := parseErr(t, `
job "example" {
  type = "batch"

  task "verify" {
    config {
      image = "alpine:latest"
    }
  }
}
`)

	for _, d := range diags {
		if d.Subject == nil {
			t.Errorf("diagnostic %q carries no source range", d.Summary)

			continue
		}

		if d.Subject.Filename != "test.vagabond.hcl" || d.Subject.Start.Line == 0 {
			t.Errorf("diagnostic %q has an unusable range: %v", d.Summary, d.Subject)
		}
	}
}

// Several problems in one file are reported together. A validator that stops at
// the first one makes an author fix a file one line per run.
func TestParse_ReportsSeveralProblemsAtOnce(t *testing.T) {
	diags := parseErr(t, `
job "example" {
  type = "batch"

  task "first" {
    config {
      image = "alpine:latest"
    }
  }

  task "second" {
    config {
      image = "alpine:latest"
    }
  }
}
`)

	if len(diags) < 2 {
		t.Errorf("got %d diagnostics, want at least 2:\n%s", len(diags), diags.Error())
	}
}

// -------------------------------------------------------------------------
// CONFIG BLOCKS
// -------------------------------------------------------------------------

// A nested block in config would fail later inside a provider plugin, where the
// author has no idea what happened. Refusing it here says so at the line.
func TestParse_RejectsNestedConfigBlocks(t *testing.T) {
	diags := parseErr(t, `
job "example" {
  type = "batch"

  task "verify" {
    driver = "container"

    config {
      image = "alpine:latest"

      limits {
        memory = 512
      }
    }
  }
}
`)

	if !strings.Contains(diags.Error(), "Blocks are not supported in config") {
		t.Errorf("diagnostics do not explain the nested block:\n%s", diags.Error())
	}

	if !strings.Contains(diags.Error(), "limits") {
		t.Errorf("diagnostics do not name the offending block:\n%s", diags.Error())
	}
}

// The config body survives decoding, which is what lets a driver read it later.
func TestParse_ConfigBodyIsUsable(t *testing.T) {
	task := parse(t, minimalJob, nil).Jobs[0].Tasks[0]

	if task.Config == nil || task.Config.Body == nil {
		t.Fatal("the config block decoded to nothing")
	}

	attrs, diags := task.Config.Body.JustAttributes()
	if diags.HasErrors() {
		t.Fatalf("reading config attributes: %s", diags.Error())
	}

	if _, ok := attrs["image"]; !ok {
		t.Errorf("config is missing image, got %v", attrs)
	}
}

// -------------------------------------------------------------------------
// METADATA
// -------------------------------------------------------------------------

func TestParse_SubstitutesMeta(t *testing.T) {
	src := `
job "example" {
  type = "batch"

  task "verify" {
    driver = "container"

    config {
      image = "alpine:latest"
    }

    source {
      type       = "git"
      repository = "https://example.com/repo.git"
      ref        = "${meta.version}"
    }
  }
}
`

	file := parse(t, src, map[string]string{"version": "abc123"})

	if got := ptr.Deref(file.Jobs[0].Tasks[0].Source.Ref); got != "abc123" {
		t.Errorf("Ref = %q, want abc123", got)
	}
}

func TestParse_EnvAndMetaDecodeToStrings(t *testing.T) {
	src := `
job "example" {
  type = "batch"

  meta {
    project = "example"
  }

  task "verify" {
    driver = "container"

    config {
      image = "alpine:latest"
    }

    env {
      CI = "true"
    }
  }
}
`

	j := parse(t, src, nil).Jobs[0]

	meta, diags := j.Meta.Attributes(nil)
	if diags.HasErrors() {
		t.Fatalf("decoding meta: %s", diags.Error())
	}

	if meta["project"] != "example" {
		t.Errorf("meta[project] = %q, want example", meta["project"])
	}

	env, diags := j.Tasks[0].Env.Attributes(nil)
	if diags.HasErrors() {
		t.Fatalf("decoding env: %s", diags.Error())
	}

	if env["CI"] != "true" {
		t.Errorf("env[CI] = %q, want true", env["CI"])
	}
}

// -------------------------------------------------------------------------
// FILES
// -------------------------------------------------------------------------

// A missing file is reported as a diagnostic, so a caller has one kind of
// failure to render rather than two.
func TestParseFile_MissingFile(t *testing.T) {
	_, diags := ParseFile(filepath.Join(t.TempDir(), "absent.vagabond.hcl"), nil)

	if !diags.HasErrors() {
		t.Fatal("reading a missing file produced no diagnostics")
	}

	if !strings.Contains(diags.Error(), "Cannot read job file") {
		t.Errorf("diagnostics do not explain the read failure:\n%s", diags.Error())
	}
}

// -------------------------------------------------------------------------
// ACCEPTANCE
// -------------------------------------------------------------------------

// A job using every block the specification defines decodes into the value
// built by hand below.
//
// Bodies are compared by the values they hold rather than by identity: a parsed
// body and a constructed one hold different concrete types and different source
// ranges, and neither difference means the jobs differ.
func TestParseFile_MatchesTheHandBuiltJob(t *testing.T) {
	file, diags := ParseFile(fixturePath, map[string]string{"version": "abc123"})
	if diags.HasErrors() {
		t.Fatalf("parsing the fixture failed: %s", diags.Error())
	}

	if len(file.Spec.Jobs) != 1 {
		t.Fatalf("len(Jobs) = %d, want 1", len(file.Spec.Jobs))
	}

	got := file.Spec.Jobs[0]
	want := expectedFixtureJob()

	// What the parser binds to each task is covered by its own tests; the
	// hand-built job describes the file.
	ignoreBinding := cmpopts.IgnoreFields(job.Task{}, "Vars", "Meta")

	if diff := cmp.Diff(want, got, cmp.Comparer(sameAttributes), ignoreBinding); diff != "" {
		t.Errorf("parsed fixture differs from the hand-built job (-want +got):\n%s", diff)
	}
}

// Every file under examples/ parses. An example that does not is worse than no
// example, and nothing else checks them now that the acceptance fixture lives
// in testdata.
func TestExamples_AllParse(t *testing.T) {
	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("reading %s: %v", examplesDir, err)
	}

	found := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".vagabond.hcl") {
			continue
		}

		found++

		t.Run(entry.Name(), func(t *testing.T) {
			path := filepath.Join(examplesDir, entry.Name())

			// Examples are parameterized, so they need values to parse.
			// Anything they declare is supplied here.
			body, diags := ParseFile(path, exampleMeta(t, path))
			if diags.HasErrors() {
				t.Errorf("example does not parse: %s", diags.Error())
			}

			if body == nil || len(body.Spec.Jobs) == 0 {
				t.Error("example decoded to no jobs")
			}
		})
	}

	if found == 0 {
		t.Errorf("no examples found in %s", examplesDir)
	}
}

// exampleMeta supplies a placeholder for every key an example declares, so that
// the parse check does not have to be updated whenever an example gains one.
func exampleMeta(t *testing.T, path string) map[string]string {
	t.Helper()

	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %s: %s", path, diags.Error())
	}

	required, diags := RequiredMeta(f.Body)
	if diags.HasErrors() {
		t.Fatalf("reading required metadata from %s: %s", path, diags.Error())
	}

	meta := make(map[string]string)

	for _, req := range required {
		for _, key := range req.Keys {
			meta[key] = "placeholder"
		}
	}

	return meta
}

// sameAttributes compares two undecoded blocks by the values they hold.
//
// Values are compared as cty rather than as strings, because a config block
// holds whatever its driver accepts: the example's args is a list, and forcing
// it through a string conversion would report every config as different.
func sameAttributes(a, b *job.RawBlock) bool {
	left, leftOK := blockValues(a)
	right, rightOK := blockValues(b)

	if !leftOK || !rightOK {
		return false
	}

	if len(left) != len(right) {
		return false
	}

	for name, leftValue := range left {
		rightValue, ok := right[name]
		if !ok || !leftValue.RawEquals(rightValue) {
			return false
		}
	}

	return true
}

// blockValues evaluates every attribute in a block, reporting whether all of
// them could be evaluated.
func blockValues(b *job.RawBlock) (map[string]cty.Value, bool) {
	if b == nil || b.Body == nil {
		return nil, true
	}

	attrs, diags := b.Body.JustAttributes()
	if diags.HasErrors() {
		return nil, false
	}

	// The expected value substitutes metadata itself, so both sides evaluate
	// against the same context the parser used.
	ctx := EvalContext(map[string]string{"version": "abc123"})
	values := make(map[string]cty.Value, len(attrs))

	for name, attr := range attrs {
		value, valueDiags := attr.Expr.Value(ctx)
		if valueDiags.HasErrors() {
			return nil, false
		}

		values[name] = value
	}

	return values, true
}

// expectedFixtureJob mirrors testdata/complete.vagabond.hcl, with
// version already substituted.
func expectedFixtureJob() job.Job {
	return job.Job{
		Name: "go-test",
		Type: ptr.Of(job.TypeBatch),
		Meta: rawBlock(`
			project = "example"
			purpose = "ci"
		`),
		Parameterized: &job.Parameterized{MetaRequired: []string{"version"}},
		Routing: &job.Routing{
			Strategy:  ptr.Of(job.StrategyFreeFirst),
			Providers: []string{"ibm-code-engine", "gcp-cloud-run"},
			MaxCost:   ptr.Of(job.Cost(0)),
			Constraints: []job.Constraint{{
				Attribute: "provider.architecture",
				Operator:  job.OperatorSetContains,
				Value:     "amd64",
			}},
			Affinities: []job.Affinity{{
				Attribute: "provider.free_quota_percent",
				Operator:  job.OperatorGreater,
				Value:     "50",
				Weight:    ptr.Of(75),
			}},
		},
		Tasks: []job.Task{{
			Name:   "test",
			Driver: job.DriverContainer,
			Config: rawBlock(`
				image   = "golang:1.27"
				command = "go"
				args    = ["test", "./..."]
			`),
			Env: rawBlock(`
				CI          = "true"
				CGO_ENABLED = "0"
			`),
			Source: &job.Source{
				Type:        "git",
				Repository:  "https://git.example.com/example/service.git",
				Ref:         ptr.Of("abc123"),
				Destination: ptr.Of("/workspace"),
			},
			WorkingDirectory: ptr.Of("/workspace"),
			Resources:        &job.Resources{CPU: ptr.Of(1000), Memory: ptr.Of(2048)},
			Timeout:          ptr.Of(job.Duration("15m")),
			Network:          &job.Network{Internet: ptr.Of(true), Private: ptr.Of(false)},
			Execution: &job.ExecutionRequirements{
				Architecture: ptr.Of(job.ArchAMD64),
				Privileged:   ptr.Of(false),
			},
			Retry: &job.Retry{
				Attempts: ptr.Of(2),
				Reroute:  ptr.Of(true),
				Backoff: &job.Backoff{
					Initial: ptr.Of(job.Duration("5s")),
					Max:     ptr.Of(job.Duration("30s")),
				},
			},
		}},
	}
}

// rawBlock builds an undecoded block from a snippet, for the expected value to
// be compared against.
func rawBlock(src string) *job.RawBlock {
	f, diags := hclsyntax.ParseConfig([]byte(src), "expected.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		panic("parsing an expected block: " + diags.Error())
	}

	return &job.RawBlock{Body: f.Body}
}
