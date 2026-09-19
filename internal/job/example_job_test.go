// -------------------------------------------------------------------------------
// Example Job Coverage
//
// Author: Alex Freidah
//
// Constructs a job using every block and attribute the specification defines,
// as a Go literal. The types have to be able to express one, and a field they
// cannot hold shows up here as a compile error, which is the earliest and
// cheapest place for it to surface.
//
// It mirrors internal/jobspec/testdata/complete.vagabond.hcl, which the parser
// is tested against, so the two stay a matched pair.
// -------------------------------------------------------------------------------

package job

import (
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/ptr"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// body parses src into an hcl.Body for the blocks Vagabond keeps undecoded.
//
// Real bodies rather than hcl.EmptyBody, because a body that carries no source
// range would not catch a change that drops range information, which is the
// property RawBlock exists to preserve.
func body(t *testing.T, src string) hcl.Body {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "example.vagabond.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing test body: %s", diags.Error())
	}

	return f.Body
}

// exampleJob is a job using every block and attribute the specification
// defines. It mirrors internal/jobspec/testdata/complete.vagabond.hcl, which is
// what the parser is tested against.
func exampleJob(t *testing.T) Job {
	t.Helper()

	return Job{
		Name: "go-test",
		Type: ptr.Of(TypeBatch),
		Meta: &RawBlock{Body: body(t, `
			project = "example"
			purpose = "ci"
		`)},
		Parameterized: &Parameterized{
			MetaRequired: []string{"version"},
		},
		Routing: &Routing{
			Strategy:  ptr.Of(StrategyFreeFirst),
			Providers: []string{"ibm-code-engine", "gcp-cloud-run"},
			MaxCost:   ptr.Of(Cost(0)),
			Constraints: []Constraint{{
				Attribute: "provider.architecture",
				Operator:  OperatorSetContains,
				Value:     "amd64",
			}},
			Affinities: []Affinity{{
				Attribute: "provider.free_quota_percent",
				Operator:  OperatorGreater,
				Value:     "50",
				Weight:    ptr.Of(75),
			}},
		},
		Tasks: []Task{{
			Name:   "test",
			Driver: DriverContainer,
			Config: &RawBlock{Body: body(t, `
				image   = "golang:1.27"
				command = "go"
				args    = ["test", "./..."]
			`)},
			Env: &RawBlock{Body: body(t, `
				CI          = "true"
				CGO_ENABLED = "0"
			`)},
			Source: &Source{
				Type:        "git",
				Repository:  "https://git.example.com/example/service.git",
				Ref:         ptr.Of("${meta.version}"),
				Destination: ptr.Of("/workspace"),
			},
			WorkingDirectory: ptr.Of("/workspace"),
			Resources:        &Resources{CPU: ptr.Of(1000), Memory: ptr.Of(2048)},
			Timeout:          ptr.Of(FromDuration(15 * time.Minute)),
			Network:          &Network{Internet: ptr.Of(true), Private: ptr.Of(false)},
			Execution: &ExecutionRequirements{
				Architecture: ptr.Of(ArchAMD64),
				Privileged:   ptr.Of(false),
			},
			Retry: &Retry{
				Attempts: ptr.Of(2),
				Reroute:  ptr.Of(true),
				Backoff: &Backoff{
					Initial: ptr.Of(FromDuration(5 * time.Second)),
					Max:     ptr.Of(FromDuration(30 * time.Second)),
				},
			},
		}},
	}
}

// -------------------------------------------------------------------------
// COVERAGE
// -------------------------------------------------------------------------

func TestExampleJob_TopLevel(t *testing.T) {
	j := exampleJob(t)

	if j.Name != "go-test" {
		t.Errorf("Name = %q, want %q", j.Name, "go-test")
	}

	if !ptr.Deref(j.Type).Valid() {
		t.Errorf("Type %q is not a valid job type", ptr.Deref(j.Type))
	}

	if len(j.Tasks) != 1 {
		t.Fatalf("len(Tasks) = %d, want 1", len(j.Tasks))
	}

	if j.Parameterized == nil || len(j.Parameterized.MetaRequired) != 1 {
		t.Fatal("parameterized meta_required did not survive construction")
	}
}

// The job declares max_cost_usd = 0, which is the guarantee the whole project
// rests on. It must read as free without any further interpretation.
func TestExampleJob_IsFree(t *testing.T) {
	j := exampleJob(t)

	if !ptr.Deref(j.Routing.MaxCost).Free() {
		t.Errorf("MaxCost %d does not report as free", ptr.Deref(j.Routing.MaxCost))
	}
}

func TestExampleJob_Routing(t *testing.T) {
	r := exampleJob(t).Routing

	if !ptr.Deref(r.Strategy).Valid() {
		t.Errorf("Strategy %q is not valid", ptr.Deref(r.Strategy))
	}

	if len(r.Providers) != 2 {
		t.Errorf("len(Providers) = %d, want 2", len(r.Providers))
	}

	if len(r.Constraints) != 1 {
		t.Errorf("len(Constraints) = %d, want 1", len(r.Constraints))
	}

	if len(r.Affinities) != 1 || ptr.Deref(r.Affinities[0].Weight) != 75 {
		t.Error("affinity did not survive construction with its weight")
	}
}

func TestExampleJob_Task(t *testing.T) {
	task := exampleJob(t).Tasks[0]

	if task.Driver != DriverContainer {
		t.Errorf("Driver = %q, want %q", task.Driver, DriverContainer)
	}

	assertDuration(t, "Timeout", ptr.Deref(task.Timeout), 15*time.Minute)

	if ptr.Deref(task.Execution.Architecture) != ArchAMD64 {
		t.Errorf("Architecture = %q, want %q", ptr.Deref(task.Execution.Architecture), ArchAMD64)
	}

	if ptr.Deref(task.Resources.CPU) != 1000 || ptr.Deref(task.Resources.Memory) != 2048 {
		t.Errorf("Resources = %+v, want cpu 1000 memory 2048", task.Resources)
	}
}

// Infrastructure failures may be rerouted; workload failures may not. The task
// declares that policy, so the type has to carry it.
func TestExampleJob_RetryPolicy(t *testing.T) {
	retry := exampleJob(t).Tasks[0].Retry

	if ptr.Deref(retry.Attempts) != 2 {
		t.Errorf("Attempts = %d, want 2", ptr.Deref(retry.Attempts))
	}

	if !ptr.Deref(retry.Reroute) {
		t.Error("Reroute = false, want true")
	}

	assertDuration(t, "Backoff.Initial", ptr.Deref(retry.Backoff.Initial), 5*time.Second)
	assertDuration(t, "Backoff.Max", ptr.Deref(retry.Backoff.Max), 30*time.Second)
}

// assertDuration parses a specification duration and compares it, so that a
// value which fails to parse is reported as that rather than as a mismatch.
func assertDuration(t *testing.T, field string, got Duration, want time.Duration) {
	t.Helper()

	parsed, err := got.Std()
	if err != nil {
		t.Fatalf("%s = %q, which does not parse: %v", field, got, err)
	}

	if parsed != want {
		t.Errorf("%s = %v, want %v", parsed, got, want)
	}
}

// The config block must keep a usable body. Losing it would mean a later decode
// error could not point at the line the author wrote.
func TestExampleJob_ConfigRetainsBody(t *testing.T) {
	config := exampleJob(t).Tasks[0].Config

	if config == nil || config.Body == nil {
		t.Fatal("config block has no body")
	}

	attrs, diags := config.Body.JustAttributes()
	if diags.HasErrors() {
		t.Fatalf("reading config attributes: %s", diags.Error())
	}

	if len(attrs) == 0 {
		t.Error("config block decoded to zero attributes")
	}
}

// Meta and env are bodies rather than maps, so that a bad value can be reported
// against its own source range. Attributes is what turns one into the string
// map a consumer actually wants.
func TestExampleJob_MetaAndEnv(t *testing.T) {
	j := exampleJob(t)

	meta, diags := j.Meta.Attributes(nil)
	if diags.HasErrors() {
		t.Fatalf("decoding meta: %s", diags.Error())
	}

	if meta["project"] != "example" {
		t.Errorf("meta[project] = %q, want %q", meta["project"], "example")
	}

	env, diags := j.Tasks[0].Env.Attributes(nil)
	if diags.HasErrors() {
		t.Fatalf("decoding env: %s", diags.Error())
	}

	if env["CI"] != "true" || env["CGO_ENABLED"] != "0" {
		t.Errorf("env = %v, want CI true and CGO_ENABLED 0", env)
	}
}

// A nil block is not an error. An absent env block and an empty one mean the
// same thing, and a caller should not have to check before asking.
func TestRawBlock_NilYieldsNothing(t *testing.T) {
	var block *RawBlock

	attrs, diags := block.Attributes(nil)
	if diags.HasErrors() {
		t.Errorf("a nil block produced diagnostics: %s", diags.Error())
	}

	if len(attrs) != 0 {
		t.Errorf("a nil block produced %d attributes", len(attrs))
	}
}

// A file is a container, so that a second job in one file is a schema error the
// parser can report rather than something silently discarded.
func TestFile_HoldsMultipleJobs(t *testing.T) {
	f := File{Jobs: []Job{exampleJob(t), {Name: "second", Type: ptr.Of(TypeBatch)}}}

	if len(f.Jobs) != 2 {
		t.Errorf("len(Jobs) = %d, want 2", len(f.Jobs))
	}
}
