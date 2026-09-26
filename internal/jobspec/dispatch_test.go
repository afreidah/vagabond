// -------------------------------------------------------------------------------
// Registration and Dispatch Metadata Tests
//
// Author: Alex Freidah
//
// What register validates with metadata standing as its own reference, what a
// dispatch may supply, and how metadata reaches a task's config and env.
// -------------------------------------------------------------------------------

package jobspec

import (
	"strings"
	"testing"
)

const optionalJob = `
job "build" {
  type = "batch"

  meta {
    team = "ci"
  }

  parameterized {
    meta_required = ["version"]
    meta_optional = ["channel"]
  }

  task "package" {
    driver = "container"

    config {
      image = "golang:${meta.version}"
    }

    env {
      MODE = "release"
    }
  }
}
`

const plainJob = `
job "nightly" {
  type = "batch"

  task "sweep" {
    driver = "container"

    config {
      image = "alpine:3.20"
    }
  }
}
`

func declared(t *testing.T, src string) Declaration {
	t.Helper()

	d, diags := Declared("test.vagabond.hcl", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("Declared() = %s", diags.Error())
	}

	return d
}

func TestDeclared(t *testing.T) {
	d := declared(t, optionalJob)

	if d.Job != "build" || !d.Parameterized {
		t.Errorf("Declared() = %+v, want parameterized build", d)
	}

	if strings.Join(d.Required, ",") != "version" || strings.Join(d.Optional, ",") != "channel" {
		t.Errorf("Required = %v, Optional = %v", d.Required, d.Optional)
	}

	if plain := declared(t, plainJob); plain.Parameterized || plain.Job != "nightly" {
		t.Errorf("Declared(plain) = %+v", plain)
	}
}

// Register validates with each declared key standing as its own reference,
// so the job passes without values and keeps its references intact.
func TestReferences_ValidateWithoutValues(t *testing.T) {
	d := declared(t, optionalJob)

	parsed, diags := Parse(Config{Source: []byte(optionalJob), Meta: d.References()})
	if diags.HasErrors() {
		t.Fatalf("Parse() = %s", diags.Error())
	}

	if found := Validate(parsed.Spec); found.HasErrors() {
		t.Fatalf("Validate() = %s", found.Error())
	}

	image, _ := parsed.Spec.Jobs[0].Tasks[0].Image(parsed.Spec.Jobs[0].Tasks[0].Vars)
	if image != "golang:${meta.version}" {
		t.Errorf("image = %q, want the reference kept as written", image)
	}
}

// A reference to a key the job never declared is a typo, caught at register.
func TestReferences_UndeclaredReferenceFails(t *testing.T) {
	typo := strings.Replace(optionalJob, "${meta.version}", "${meta.verison}", 1)
	d := declared(t, typo)

	if _, diags := Parse(Config{Source: []byte(typo), Meta: d.References()}); !diags.HasErrors() {
		t.Error("an undeclared reference parsed at register")
	}
}

func TestCheckDispatch(t *testing.T) {
	parameterized := declared(t, optionalJob)
	plain := declared(t, plainJob)

	tests := []struct {
		name     string
		decl     Declaration
		supplied map[string]string
		want     string
	}{
		{name: "required only", decl: parameterized, supplied: map[string]string{"version": "1"}},
		{name: "required and optional", decl: parameterized, supplied: map[string]string{"version": "1", "channel": "beta"}},
		{name: "plain with none", decl: plain},
		{name: "plain with some", decl: plain, supplied: map[string]string{"x": "1"}, want: "not parameterized"},
		{name: "unpermitted", decl: parameterized, supplied: map[string]string{"version": "1", "branch": "main"}, want: "does not declare metadata branch"},
		{name: "missing required", decl: parameterized, supplied: map[string]string{"channel": "beta"}, want: "requires metadata that was not supplied: version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.decl.CheckDispatch(tt.supplied)

			switch {
			case tt.want == "" && err != nil:
				t.Errorf("CheckDispatch() = %v, want nil", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("CheckDispatch() = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// A task sees supplied values in its config, and its metadata, the job's meta
// block overridden by what was supplied, as VAGABOND_META_* in its env.
func TestParse_BindsMetaToTasks(t *testing.T) {
	parsed, diags := Parse(Config{
		Source: []byte(optionalJob),
		Meta:   map[string]string{"version": "1.27", "team": "platform"},
	})
	if diags.HasErrors() {
		t.Fatalf("Parse() = %s", diags.Error())
	}

	task := &parsed.Spec.Jobs[0].Tasks[0]

	if image, _ := task.Image(task.Vars); image != "golang:1.27" {
		t.Errorf("image = %q, want golang:1.27", image)
	}

	env, envDiags := task.Environment()
	if envDiags.HasErrors() {
		t.Fatalf("Environment() = %s", envDiags.Error())
	}

	want := map[string]string{
		"MODE":                  "release",
		"VAGABOND_META_VERSION": "1.27",
		"VAGABOND_META_TEAM":    "platform",
	}

	for key, value := range want {
		if env[key] != value {
			t.Errorf("env[%s] = %q, want %q", key, env[key], value)
		}
	}
}
