// -------------------------------------------------------------------------------
// Container Tasks Tests
//
// Author: Alex Freidah
//
// A container task read once for every container platform: its image, command,
// arguments, environment, working directory and timeout as written, its size
// with the provider's defaults for what it does not declare, and the task's own
// faults refused as internal.
// -------------------------------------------------------------------------------

package plugin

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/job"
)

// block parses src as the body of a config or env block.
func block(t *testing.T, src string) *job.RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing %q: %s", src, diags.Error())
	}

	return &job.RawBlock{Body: f.Body}
}

// A task's container is everything it wrote, its size what it declared and
// the defaults for the rest.
func TestContainerOf(t *testing.T) {
	task := &job.Task{
		Name:             "build",
		Config:           block(t, "image = \"golang:1.27\"\ncommand = \"go\"\nargs = [\"test\", \"./...\"]"),
		Env:              block(t, `CGO_ENABLED = "0"`),
		WorkingDirectory: new("/src"),
		Resources:        &job.Resources{Memory: new(4096)},
		Timeout:          new(job.Duration("10m")),
	}

	got, err := ContainerOf(task, Resources{CPU: 1000, Memory: 2048})
	if err != nil {
		t.Fatalf("ContainerOf() = %v", err)
	}

	if got.Image != "golang:1.27" || !slices.Equal(got.Command, []string{"go"}) ||
		!slices.Equal(got.Args, []string{"test", "./..."}) || got.Env["CGO_ENABLED"] != "0" ||
		got.WorkingDir != "/src" || got.Timeout != 10*time.Minute {
		t.Errorf("ContainerOf() = %+v", got)
	}

	if got.CPU != 1000 || got.Memory != 4096 {
		t.Errorf("size = %d millicores and %d MiB, want the default CPU and declared memory", got.CPU, got.Memory)
	}
}

// A task with no image, or a timeout that does not parse, is its own fault,
// which no provider would run.
func TestContainerOf_RefusesTheTasksOwnFaults(t *testing.T) {
	image := block(t, `image = "alpine:3.20"`)
	faults := map[string]*job.Task{
		"no image":  {Name: "build", Config: block(t, `command = "make"`)},
		"a timeout": {Name: "build", Config: image, Timeout: new(job.Duration("soon"))},
	}

	for name, task := range faults {
		_, err := ContainerOf(task, Resources{})
		if failure, ok := errors.AsType[*Error](err); !ok || failure.Class != ClassInternal {
			t.Errorf("ContainerOf() with %s = %v, want an internal failure", name, err)
		}
	}
}

// Or fills only what a size leaves zero.
func TestResources_Or(t *testing.T) {
	got := Resources{Memory: 512}.Or(Resources{CPU: 1000, Memory: 2048})
	if got != (Resources{CPU: 1000, Memory: 512}) {
		t.Errorf("Or() = %+v, want the fallback CPU and the declared memory", got)
	}
}
