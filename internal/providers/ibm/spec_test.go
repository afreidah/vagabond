// -------------------------------------------------------------------------------
// Task to Job Run Tests
//
// Author: Alex Freidah
//
// The translation's three load-bearing settings: the size a task rounds up to,
// retries left off, and no identity for the workload. Plus the timeout, the
// environment and the command line, which have to arrive exactly as declared.
// -------------------------------------------------------------------------------

package ibm

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// RESOURCES
// -------------------------------------------------------------------------

// A task gets the smallest size holding what it declared, with memory compared
// in bytes since Code Engine's G is decimal.
func TestSizeFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		resources   *job.Resources
		cpu, memory string
	}{
		{"defaults", nil, "1", "4G"},
		{"smallest", &job.Resources{CPU: new(100), Memory: new(128)}, "0.125", "0.25G"},
		{"exact decimal fit", &job.Resources{CPU: new(1000), Memory: new(3814)}, "1", "4G"},
		{"a MiB past 4G", &job.Resources{CPU: new(1000), Memory: new(3815)}, "1", "8G"},
		{"4096 MiB", &job.Resources{CPU: new(1000), Memory: new(4096)}, "1", "8G"},
		{"memory forces more CPU", &job.Resources{CPU: new(250), Memory: new(8192)}, "2", "16G"},
		{"between CPU steps", &job.Resources{CPU: new(1500), Memory: new(1024)}, "2", "4G"},
		{"largest", &job.Resources{CPU: new(12000), Memory: new(45776)}, "12", "48G"},
		{"zeroes take the defaults", &job.Resources{CPU: new(0), Memory: new(0)}, "1", "4G"},
	}

	for _, tc := range cases {
		got, err := sizeFor(&job.Task{Name: "test", Resources: tc.resources})
		if err != nil {
			t.Errorf("%s: sizeFor() = %v", tc.name, err)

			continue
		}

		if got.cpu() != tc.cpu || got.memory() != tc.memory {
			t.Errorf("%s: sizeFor() = %s / %s, want %s / %s", tc.name, got.cpu(), got.memory(), tc.cpu, tc.memory)
		}
	}
}

// A task past the largest size has nothing to round up to.
func TestSizeFor_PastTheLargest(t *testing.T) {
	t.Parallel()

	for _, resources := range []*job.Resources{
		{CPU: new(12001)},
		{Memory: new(45777)},
		{CPU: new(12001), Memory: new(1024)},
	} {
		if got, err := sizeFor(&job.Task{Name: "test", Resources: resources}); err == nil {
			t.Errorf("sizeFor(%+v) = %s / %s, want an error", resources, got.cpu(), got.memory())
		}
	}
}

// -------------------------------------------------------------------------
// TIMEOUT
// -------------------------------------------------------------------------

// The timeout rounds up to whole seconds, at least one, and defaults to 24h.
func TestExecutionSeconds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		timeout *job.Duration
		want    int
	}{
		{nil, 86400},
		{new(job.Duration("15m")), 900},
		{new(job.Duration("1500ms")), 2},
		{new(job.Duration("10ms")), 1},
		{new(job.Duration("24h")), 86400},
	}

	for _, tc := range cases {
		got, err := executionSeconds(&job.Task{Name: "test", Timeout: tc.timeout})
		if err != nil || got != tc.want {
			t.Errorf("executionSeconds(%v) = %d, %v; want %d", tc.timeout, got, err, tc.want)
		}
	}
}

// A timeout past Code Engine's maximum is an error, not a shorter run.
func TestExecutionSeconds_PastTheMaximum(t *testing.T) {
	t.Parallel()

	if _, err := executionSeconds(&job.Task{Name: "test", Timeout: new(job.Duration("25h"))}); err == nil {
		t.Fatal("a 25h timeout was accepted")
	}
}

// -------------------------------------------------------------------------
// THE SPEC
// -------------------------------------------------------------------------

// The encoded job run carries the task, retries off and present as zero, one
// instance, and no identity for the workload.
func TestBuildSpec(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)
	p.cfg.ImageSecret = new("ghcr")

	spec, err := p.buildSpec("vagabond-run", containerTask(t))
	if err != nil {
		t.Fatalf("buildSpec() = %v", err)
	}

	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("encoding the spec: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decoding the spec: %v", err)
	}

	want := map[string]any{
		"name":                     "vagabond-run",
		"image_reference":          "golang:1.27",
		"image_secret":             "ghcr",
		"run_mode":                 "task",
		"run_service_account":      "none",
		"scale_cpu_limit":          "1",
		"scale_memory_limit":       "4G",
		"scale_max_execution_time": float64(900),
		"scale_retry_limit":        float64(0),
		"scale_array_spec":         "0",
	}

	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}

	if _, ok := got["scale_retry_limit"]; !ok {
		t.Error("scale_retry_limit is absent, so Code Engine would retry three times")
	}

	commands, _ := got["run_commands"].([]any)
	arguments, _ := got["run_arguments"].([]any)

	if len(commands) != 1 || commands[0] != "go" || len(arguments) != 2 || arguments[1] != "./..." {
		t.Errorf("commands %v, arguments %v", commands, arguments)
	}

	env, _ := got["run_env_variables"].([]any)
	if len(env) != 2 {
		t.Fatalf("env = %v, want CGO_ENABLED and CI", env)
	}

	first, _ := env[0].(map[string]any)
	if first["name"] != "CGO_ENABLED" || first["type"] != "literal" || first["value"] != "0" {
		t.Errorf("env[0] = %v, want CGO_ENABLED sorted first, literal", first)
	}
}

// Without an image secret, none is sent.
func TestBuildSpec_NoImageSecret(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)

	spec, err := p.buildSpec("vagabond-run", containerTask(t))
	if err != nil {
		t.Fatalf("buildSpec() = %v", err)
	}

	if spec.ImageSecret != "" {
		t.Errorf("ImageSecret = %q, want none", spec.ImageSecret)
	}
}

// Submission metadata reaches the run: substituted into the config and set as
// VAGABOND_META_* in the environment.
func TestBuildSpec_CarriesMetadata(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)

	parsed, diags := jobspec.Parse(jobspec.Config{
		Source: []byte(`
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
`),
		Meta: map[string]string{"version": "1.27"},
	})
	if diags.HasErrors() {
		t.Fatalf("Parse() = %s", diags.Error())
	}

	spec, err := p.buildSpec("vagabond-run", &parsed.Spec.Jobs[0].Tasks[0])
	if err != nil {
		t.Fatalf("buildSpec() = %v", err)
	}

	if spec.Image != "golang:1.27" {
		t.Errorf("Image = %q, want golang:1.27", spec.Image)
	}

	if len(spec.Env) != 1 || spec.Env[0].Name != "VAGABOND_META_VERSION" || spec.Env[0].Value != "1.27" {
		t.Errorf("Env = %v, want VAGABOND_META_VERSION=1.27", spec.Env)
	}
}

// Each untranslatable task fails as internal, so it is not rerouted to a
// provider that fails the same way.
func TestBuildSpec_Refusals(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)

	cases := map[string]*job.Task{
		"no image": {Name: "test", Driver: job.DriverContainer, Config: rawBlock(t, "command = \"true\"\n")},
		"unreadable env": {
			Name: "test", Driver: job.DriverContainer,
			Config: rawBlock(t, "image = \"alpine:3.20\"\n"),
			Env:    rawBlock(t, "LIST = [\"a\", \"b\"]\n"),
		},
		"too large": {
			Name: "test", Driver: job.DriverContainer,
			Config:    rawBlock(t, "image = \"alpine:3.20\"\n"),
			Resources: &job.Resources{CPU: new(16000)},
		},
		"timeout too long": {
			Name: "test", Driver: job.DriverContainer,
			Config:  rawBlock(t, "image = \"alpine:3.20\"\n"),
			Timeout: new(job.Duration("48h")),
		},
		"env takes a carried name": {
			Name: "test", Driver: job.DriverContainer,
			Config: rawBlock(t, "image = \"alpine:3.20\"\nargs = [\"a\\nb\"]\n"),
			Env:    rawBlock(t, "VAGABOND_ARG_0 = \"mine\"\n"),
		},
	}

	for name, task := range cases {
		if _, err := p.buildSpec("vagabond-run", task); err == nil || plugin.Reroutable(err) {
			t.Errorf("%s: buildSpec() = %v, want an internal failure", name, err)
		}
	}
}

// -------------------------------------------------------------------------
// THE COMMAND LINE
// -------------------------------------------------------------------------

// A one-line command or argument is sent escaped, and one holding a line break
// is carried in env and sent as $(name). Env values are escaped too.
func TestBuildSpec_CommandLine(t *testing.T) {
	t.Parallel()

	_, p := newFakeIBM(t)

	literal := func(name, value string) envVar { return envVar{Type: "literal", Name: name, Value: value} }

	cases := []struct {
		name      string
		config    string
		env       string
		commands  []string
		arguments []string
		vars      []envVar
	}{
		{
			name:      "one line is escaped",
			config:    `command = "sh"` + "\n" + `args = ["-c", "echo $HOME $(PWD)"]`,
			env:       `PRICE = "$5"`,
			commands:  []string{"sh"},
			arguments: []string{"-c", "echo $$HOME $$(PWD)"},
			vars:      []envVar{literal("PRICE", "$$5")},
		},
		{
			name:      "a newline is carried after the task's env",
			config:    `command = "sh"` + "\n" + `args = ["-ec", "set -x\necho $HOME"]`,
			env:       `CI = "true"`,
			commands:  []string{"sh"},
			arguments: []string{"-ec", "$(VAGABOND_ARG_1)"},
			vars:      []envVar{literal("CI", "true"), literal("VAGABOND_ARG_1", "set -x\necho $$HOME")},
		},
		{
			name:      "a carriage return is carried",
			config:    `args = ["a\rb"]`,
			arguments: []string{"$(VAGABOND_ARG_0)"},
			vars:      []envVar{literal("VAGABOND_ARG_0", "a\rb")},
		},
		{
			name:     "a multi-line command is carried",
			config:   `command = "a\nb"`,
			commands: []string{"$(VAGABOND_COMMAND)"},
			vars:     []envVar{literal("VAGABOND_COMMAND", "a\nb")},
		},
		{
			name:      "a carried name the task sets is its own when nothing is carried",
			config:    `args = ["one line"]`,
			env:       `VAGABOND_ARG_0 = "mine"`,
			arguments: []string{"one line"},
			vars:      []envVar{literal("VAGABOND_ARG_0", "mine")},
		},
	}

	for _, tc := range cases {
		spec, err := p.buildSpec("vagabond-run", &job.Task{
			Name:   "test",
			Driver: job.DriverContainer,
			Config: rawBlock(t, "image = \"alpine:3.20\"\n"+tc.config+"\n"),
			Env:    rawBlock(t, tc.env+"\n"),
		})
		if err != nil {
			t.Errorf("%s: buildSpec() = %v", tc.name, err)

			continue
		}

		if !slices.Equal(spec.Commands, tc.commands) || !slices.Equal(spec.Arguments, tc.arguments) {
			t.Errorf("%s: commands %q, arguments %q; want %q, %q",
				tc.name, spec.Commands, spec.Arguments, tc.commands, tc.arguments)
		}

		if !slices.Equal(spec.Env, tc.vars) {
			t.Errorf("%s: env %q, want %q", tc.name, spec.Env, tc.vars)
		}
	}
}

// Doubling every $ is what Kubernetes' halving gives back as the original.
func TestEscape(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":           "",
		"no dollars": "no dollars",
		"$HOME":      "$$HOME",
		"$(NAME)":    "$$(NAME)",
		"$$":         "$$$$",
		"trailing $": "trailing $$",
	}

	for in, want := range cases {
		if got := escape(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
}
