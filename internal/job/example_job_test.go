// -------------------------------------------------------------------------------
// Example Job Coverage
//
// Author: Alex Freidah
//
// Constructs examples/terraform-verify.vagabond.hcl as a Go literal. The
// specification types have to express the documented example in full, and this
// is what proves it before a parser exists to do so end to end.
//
// A field the example uses that these types cannot hold shows up here as a
// compile error, which is the earliest and cheapest place for it to surface.
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

// exampleJob mirrors examples/terraform-verify.vagabond.hcl.
func exampleJob(t *testing.T) Job {
	t.Helper()

	return Job{
		Name: "terraform-verify",
		Type: ptr.Of(TypeBatch),
		Meta: map[string]string{
			"project":    "munchbox",
			"repository": "afreidah/munchbox",
			"purpose":    "ci",
		},
		Parameterized: &Parameterized{
			MetaRequired: []string{"git_ref"},
		},
		Routing: &Routing{
			Strategy:  ptr.Of(StrategyFreeFirst),
			Providers: []string{"ibm-code-engine", "gcp-cloud-run"},
			MaxCost:   ptr.Of(Cost(0)),
			Constraints: []Constraint{{
				Attribute: "provider.architecture",
				Operator:  "=",
				Value:     "amd64",
			}},
			Affinities: []Affinity{{
				Attribute: "provider.free_quota_percent",
				Operator:  ">",
				Value:     "50",
				Weight:    ptr.Of(75),
			}},
		},
		Tasks: []Task{{
			Name:   "verify",
			Driver: DriverContainer,
			Config: &RawBlock{Body: body(t, `
				image   = "hashicorp/terraform:latest"
				command = "sh"
				args    = ["-lc", "terraform fmt -check -recursive && terraform validate"]
			`)},
			Env: map[string]string{
				"CI":               "true",
				"TF_IN_AUTOMATION": "true",
			},
			Source: &Source{
				Type:        "git",
				Repository:  "https://github.com/afreidah/munchbox.git",
				Ref:         ptr.Of("${meta.git_ref}"),
				Destination: ptr.Of("/workspace"),
			},
			WorkingDirectory: ptr.Of("/workspace/infrastructure/terragrunt"),
			Resources:        &Resources{CPU: ptr.Of(1000), Memory: ptr.Of(2048)},
			Timeout:          ptr.Of(Duration(15 * time.Minute)),
			Network:          &Network{Internet: ptr.Of(true), Private: ptr.Of(false)},
			Execution: &ExecutionRequirements{
				Architecture: ptr.Of(ArchAMD64),
				Privileged:   ptr.Of(false),
			},
			Retry: &Retry{
				Attempts: ptr.Of(2),
				Reroute:  ptr.Of(true),
				Backoff: &Backoff{
					Initial: ptr.Of(Duration(5 * time.Second)),
					Max:     ptr.Of(Duration(30 * time.Second)),
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

	if j.Name != "terraform-verify" {
		t.Errorf("Name = %q, want %q", j.Name, "terraform-verify")
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

	if ptr.Deref(task.Timeout).Std() != 15*time.Minute {
		t.Errorf("Timeout = %v, want 15m", ptr.Deref(task.Timeout).Std())
	}

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

	if ptr.Deref(retry.Backoff.Initial).Std() != 5*time.Second {
		t.Errorf("Backoff.Initial = %v, want 5s", ptr.Deref(retry.Backoff.Initial).Std())
	}

	if ptr.Deref(retry.Backoff.Max).Std() != 30*time.Second {
		t.Errorf("Backoff.Max = %v, want 30s", ptr.Deref(retry.Backoff.Max).Std())
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

// Meta and env are ordinary string maps rather than undecoded bodies, because
// their values have a known type even though their keys are chosen by the
// author. Only config is genuinely dynamic.
func TestExampleJob_MetaAndEnv(t *testing.T) {
	j := exampleJob(t)

	if j.Meta["project"] != "munchbox" {
		t.Errorf("Meta[project] = %q, want %q", j.Meta["project"], "munchbox")
	}

	env := j.Tasks[0].Env
	if env["CI"] != "true" || env["TF_IN_AUTOMATION"] != "true" {
		t.Errorf("Env = %v, want CI and TF_IN_AUTOMATION set to true", env)
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
