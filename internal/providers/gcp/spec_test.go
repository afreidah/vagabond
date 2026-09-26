// -------------------------------------------------------------------------------
// Task Translation Tests
//
// Author: Alex Freidah
//
// Vagabond's units are not Cloud Run's, and every one of these conversions is
// a place where a task could quietly run with less than it asked for.
// -------------------------------------------------------------------------------

package gcp

import (
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/ptr"
)

// Rounding up, because giving a task less CPU than it asked for produces a
// mysterious timeout rather than a bill.
func TestVCPURoundsUp(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		millicores int
		want       int
	}{
		"a quarter core still gets one": {250, 1},
		"exactly one":                   {1000, 1},
		"just over one gets two":        {1001, 2},
		"one and a half gets two":       {1500, 2},
		"the ceiling":                   {maxCPU, 8},
		"nothing still gets one":        {0, 1},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := vCPU(tc.millicores); got != tc.want {
				t.Errorf("vCPU(%d) = %d, want %d", tc.millicores, got, tc.want)
			}
		})
	}
}

// A task that states nothing gets the ratio at which the free tier's two
// dimensions exhaust together.
func TestResourceLimitsDefaultToTheFreeTierRatio(t *testing.T) {
	t.Parallel()

	limits := resourceLimits(&job.Task{Name: "test"})

	if limits["cpu"] != "1" || limits["memory"] != "2048Mi" {
		t.Errorf("limits = %v, want one vCPU against 2048Mi", limits)
	}
}

// Zero is not a request for nothing, it is a task that said nothing.
func TestResourceLimitsIgnoresZeroes(t *testing.T) {
	t.Parallel()

	limits := resourceLimits(&job.Task{
		Name:      "test",
		Resources: &job.Resources{CPU: ptr.Of(0), Memory: ptr.Of(0)},
	})

	if limits["cpu"] != "1" || limits["memory"] != "2048Mi" {
		t.Errorf("limits = %v, want the defaults", limits)
	}
}

// Absent means Cloud Run's own maximum, not zero, which would be a job killed
// the moment it started.
func TestTaskTimeoutDefaultsToTheMaximum(t *testing.T) {
	t.Parallel()

	got, err := taskTimeout(&job.Task{Name: "test"})
	if err != nil {
		t.Fatalf("taskTimeout failed: %v", err)
	}

	if got != maxDuration {
		t.Errorf("timeout = %s, want %s", got, maxDuration)
	}
}

func TestTaskTimeoutReadsTheTask(t *testing.T) {
	t.Parallel()

	got, err := taskTimeout(&job.Task{
		Name:    "test",
		Timeout: ptr.Of(job.Duration("90m")),
	})
	if err != nil {
		t.Fatalf("taskTimeout failed: %v", err)
	}

	if want := 90 * time.Minute; got != want {
		t.Errorf("timeout = %s, want %s", got, want)
	}
}

// Submission metadata reaches Cloud Run: substituted into the config and set
// as VAGABOND_META_* in the container's environment.
func TestJobSpecCarriesMetadata(t *testing.T) {
	t.Parallel()

	_, p := newFakeGoogle(t)

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

	spec, err := p.jobSpec(&parsed.Spec.Jobs[0].Tasks[0])
	if err != nil {
		t.Fatalf("jobSpec() = %v", err)
	}

	template := spec["template"].(map[string]any)["template"].(map[string]any)
	container := template["containers"].([]map[string]any)[0]

	if container["image"] != "golang:1.27" {
		t.Errorf("image = %v, want golang:1.27", container["image"])
	}

	env := container["env"].([]map[string]string)
	if len(env) != 1 || env[0]["name"] != "VAGABOND_META_VERSION" || env[0]["value"] != "1.27" {
		t.Errorf("env = %v, want VAGABOND_META_VERSION=1.27", env)
	}
}

// A task with no image cannot be translated, and saying so as an internal
// failure keeps it from being rerouted to a provider that would fail the same
// way.
func TestJobSpecNeedsAnImage(t *testing.T) {
	t.Parallel()

	_, p := newFakeGoogle(t)

	_, err := p.jobSpec(&job.Task{
		Name:   "test",
		Driver: job.DriverContainer,
		Config: rawBlock(t, "command = \"true\"\n"),
	})
	if err == nil {
		t.Fatal("a task with no image was translated")
	}
}
