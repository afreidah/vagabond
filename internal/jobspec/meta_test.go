// -------------------------------------------------------------------------------
// Job Metadata Tests
//
// Author: Alex Freidah
//
// The behaviour worth pinning is that an unsupplied value is refused rather
// than substituted. Nomad refuses the same case, and the alternative is a job
// that validates and then interpolates nothing at submission.
// -------------------------------------------------------------------------------

package jobspec

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/ptr"
)

const parameterizedJob = `
job "build" {
  type = "batch"

  parameterized {
    meta_required = ["version"]
  }

  task "package" {
    driver = "container"

    config {
      image   = "alpine:latest"
      command = "sh"
      args    = ["-lc", "echo building"]
    }

    source {
      type       = "git"
      repository = "https://git.example.com/example/app.git"
      ref        = "${meta.version}"
    }
  }
}
`

func bodyOf(t *testing.T, src string) hcl.Body {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "test.vagabond.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing: %s", diags.Error())
	}

	return f.Body
}

// -------------------------------------------------------------------------
// DECLARED METADATA
// -------------------------------------------------------------------------

func TestRequiredMeta(t *testing.T) {
	required, diags := RequiredMeta(bodyOf(t, parameterizedJob))
	if diags.HasErrors() {
		t.Fatalf("RequiredMeta returned diagnostics: %s", diags.Error())
	}

	if len(required) != 1 {
		t.Fatalf("recorded %d requirements, want 1", len(required))
	}

	req := required[0]

	if req.Job != "build" {
		t.Errorf("Job = %q, want build", req.Job)
	}

	if len(req.Keys) != 1 || req.Keys[0] != "version" {
		t.Errorf("Keys = %v, want [version]", req.Keys)
	}

	// The range is what lets a missing-metadata message point at the line that
	// asked for it rather than at the expression that first referenced one.
	if req.Range.Start.Line == 0 {
		t.Error("the requirement carries no source range")
	}
}

// A job with no parameterized block requires nothing, which is the common case
// and must not be reported as an empty requirement.
func TestRequiredMeta_NoParameterizedBlock(t *testing.T) {
	required, diags := RequiredMeta(bodyOf(t, minimalJob))
	if diags.HasErrors() {
		t.Fatalf("RequiredMeta returned diagnostics: %s", diags.Error())
	}

	if len(required) != 0 {
		t.Errorf("required = %v, want nothing", required)
	}
}

// A file may hold several jobs, and a caller deciding what to supply needs to
// know which job wanted what.
func TestRequiredMeta_PerJob(t *testing.T) {
	src := parameterizedJob + `
job "deploy" {
  type = "batch"

  parameterized {
    meta_required = ["environment", "version"]
  }

  task "apply" {
    driver = "container"

    config {
      image = "alpine:latest"
    }
  }
}
`

	required, diags := RequiredMeta(bodyOf(t, src))
	if diags.HasErrors() {
		t.Fatalf("RequiredMeta returned diagnostics: %s", diags.Error())
	}

	if len(required) != 2 {
		t.Fatalf("recorded %d requirements, want 2", len(required))
	}

	// File order, not map order, so a report follows the file.
	if required[0].Job != "build" || required[1].Job != "deploy" {
		t.Errorf("requirements are out of file order: %q then %q",
			required[0].Job, required[1].Job)
	}

	if len(required[1].Keys) != 2 {
		t.Errorf("deploy requires %v, want two keys", required[1].Keys)
	}
}

// A requirement list that is not strings is a job file error, not something to
// silently skip.
func TestRequiredMeta_RejectsNonStringKeys(t *testing.T) {
	src := `
job "build" {
  type = "batch"

  parameterized {
    meta_required = [1, 2]
  }
}
`

	_, diags := RequiredMeta(bodyOf(t, src))
	if !diags.HasErrors() {
		t.Fatal("a numeric meta_required entry produced no diagnostics")
	}

	if !strings.Contains(diags.Error(), "must be a string") {
		t.Errorf("diagnostics do not explain the problem:\n%s", diags.Error())
	}
}

// -------------------------------------------------------------------------
// ENFORCEMENT
// -------------------------------------------------------------------------

func TestCheckRequiredMeta(t *testing.T) {
	tests := []struct {
		name     string
		required []Requirement
		supplied map[string]string
		wantErr  bool
	}{
		{
			name:     "nothing required",
			required: nil,
			supplied: nil,
		},
		{
			name:     "required and supplied",
			required: []Requirement{{Job: "build", Keys: []string{"version"}}},
			supplied: map[string]string{"version": "1.4.2"},
		},
		{
			name:     "required and missing",
			required: []Requirement{{Job: "build", Keys: []string{"version"}}},
			supplied: nil,
			wantErr:  true,
		},
		{
			name:     "one of two missing",
			required: []Requirement{{Job: "deploy", Keys: []string{"environment", "version"}}},
			supplied: map[string]string{"version": "1.4.2"},
			wantErr:  true,
		},
		{
			name:     "an explicitly empty value counts as supplied",
			required: []Requirement{{Job: "build", Keys: []string{"version"}}},
			supplied: map[string]string{"version": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := CheckRequiredMeta(tt.required, tt.supplied)

			if tt.wantErr && !diags.HasErrors() {
				t.Error("expected diagnostics, got none")
			}

			if !tt.wantErr && diags.HasErrors() {
				t.Errorf("unexpected diagnostics: %s", diags.Error())
			}
		})
	}
}

// The message has to name the job, the missing keys, and how to supply them.
// An author who has to consult the README has been failed by the error.
func TestCheckRequiredMeta_MessageIsActionable(t *testing.T) {
	diags := CheckRequiredMeta(
		[]Requirement{{Job: "deploy", Keys: []string{"environment", "version"}}},
		map[string]string{"version": "1.4.2"},
	)

	msg := diags.Error()

	for _, want := range []string{"deploy", "environment", "-meta"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}

	if strings.Contains(msg, "version") && !strings.Contains(msg, "environment, version") {
		t.Errorf("message names a key that was supplied:\n%s", msg)
	}
}

// -------------------------------------------------------------------------
// THROUGH THE PARSER
// -------------------------------------------------------------------------

// Matching Nomad: a job referencing metadata nobody supplied is refused rather
// than carrying a placeholder that would interpolate to nothing at submission.
func TestParse_UnsuppliedMetaFails(t *testing.T) {
	_, diags := Parse(Config{
		Filename: "test.vagabond.hcl",
		Source:   []byte(parameterizedJob),
	})

	if !diags.HasErrors() {
		t.Fatal("a job with unsupplied metadata parsed cleanly")
	}

	if !strings.Contains(diags.Error(), "version") {
		t.Errorf("diagnostics do not name the missing key:\n%s", diags.Error())
	}
}

func TestParse_SuppliedMetaSucceeds(t *testing.T) {
	file, diags := Parse(Config{
		Filename: "test.vagabond.hcl",
		Source:   []byte(parameterizedJob),
		Meta:     map[string]string{"version": "1.4.2"},
	})

	if diags.HasErrors() {
		t.Fatalf("parsing failed: %s", diags.Error())
	}

	if got := ptr.Deref(file.Jobs[0].Tasks[0].Source.Ref); got != "1.4.2" {
		t.Errorf("Ref = %q, want 1.4.2", got)
	}
}

// A reference to a key nobody declared or supplied fails at the reference
// itself, which is the whole reason only supplied keys are registered. This is
// the shape a typo takes.
func TestParse_UndeclaredMetaFailsAtTheReference(t *testing.T) {
	src := strings.Replace(parameterizedJob, "${meta.version}", "${meta.vrsion}", 1)

	_, diags := Parse(Config{
		Filename: "test.vagabond.hcl",
		Source:   []byte(src),
		Meta:     map[string]string{"version": "1.4.2"},
	})

	if !diags.HasErrors() {
		t.Fatal("a reference to an undeclared key parsed cleanly")
	}

	if !strings.Contains(diags.Error(), "vrsion") {
		t.Errorf("diagnostics do not name the offending key:\n%s", diags.Error())
	}

	for _, d := range diags {
		if d.Subject == nil {
			t.Errorf("diagnostic %q carries no source range", d.Summary)
		}
	}
}

// -------------------------------------------------------------------------
// EVALUATION CONTEXT
// -------------------------------------------------------------------------

// Only supplied keys are registered, which is what makes an unsupplied
// reference reportable rather than silently empty.
func TestEvalContext_RegistersOnlySuppliedKeys(t *testing.T) {
	ctx := EvalContext(map[string]string{"version": "1.4.2"})

	meta, ok := ctx.Variables[MetaNamespace]
	if !ok {
		t.Fatalf("the %s namespace is not registered", MetaNamespace)
	}

	attrs := meta.AsValueMap()
	if len(attrs) != 1 {
		t.Errorf("registered %d keys, want 1", len(attrs))
	}

	if attrs["version"].AsString() != "1.4.2" {
		t.Errorf("version = %q, want 1.4.2", attrs["version"].AsString())
	}
}

// An empty object rather than a null, because a reference into a null fails
// with a message about nullness rather than about a missing key.
func TestEvalContext_EmptyMetaIsAnObject(t *testing.T) {
	ctx := EvalContext(nil)

	meta := ctx.Variables[MetaNamespace]
	if meta.IsNull() {
		t.Error("the meta namespace is null with no metadata supplied")
	}

	if !meta.Type().IsObjectType() {
		t.Errorf("meta is %s, want an object", meta.Type().FriendlyName())
	}
}
