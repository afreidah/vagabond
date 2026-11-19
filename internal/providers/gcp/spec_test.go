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

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// RESOURCES
// -------------------------------------------------------------------------

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

// podOf builds the job for a task with the given resources and timeout, and
// returns its one container's limits and its template's timeout.
func podOf(t *testing.T, resources *job.Resources, timeout *job.Duration) (limits map[string]string, seconds string) {
	t.Helper()

	_, p := newFakeGoogle(t)

	spec, err := p.jobSpec(&job.Task{
		Name:      "test",
		Driver:    job.DriverContainer,
		Config:    rawBlock(t, "image = \"alpine:3.20\"\n"),
		Resources: resources,
		Timeout:   timeout,
	})
	if err != nil {
		t.Fatalf("jobSpec() = %v", err)
	}

	template := spec["template"].(map[string]any)["template"].(map[string]any)
	container := template["containers"].([]map[string]any)[0]

	return container["resources"].(map[string]any)["limits"].(map[string]string), template["timeout"].(string)
}

// A task that states nothing, or zero, which is saying nothing, gets the ratio
// at which the free tier's two dimensions exhaust together, and Cloud Run's
// maximum run, not zero, which would end the job as it started.
func TestJobSpecDefaultsToTheFreeTierRatio(t *testing.T) {
	t.Parallel()

	for _, resources := range []*job.Resources{nil, {CPU: new(0), Memory: new(0)}} {
		limits, timeout := podOf(t, resources, nil)

		if limits["cpu"] != "1" || limits["memory"] != "2048Mi" || timeout != "86400s" {
			t.Errorf("limits = %v and timeout %s, want one vCPU against 2048Mi for a day", limits, timeout)
		}
	}
}

// What a task declares is used, its CPU rounded up to whole vCPUs.
func TestJobSpecReadsTheTask(t *testing.T) {
	t.Parallel()

	limits, timeout := podOf(t, &job.Resources{CPU: new(1500), Memory: new(4096)}, new(job.Duration("90m")))

	if limits["cpu"] != "2" || limits["memory"] != "4096Mi" || timeout != "5400s" {
		t.Errorf("limits = %v and timeout %s, want 2 vCPU, 4096Mi and 90 minutes", limits, timeout)
	}
}

// The task's working directory becomes the container's, and a task without one
// leaves the image's in place.
func TestJobSpecSetsTheWorkingDirectory(t *testing.T) {
	t.Parallel()

	_, p := newFakeGoogle(t)

	cases := []struct {
		dir  *string
		want any
	}{
		{new("/workspace"), "/workspace"},
		{nil, nil},
	}
	for _, tc := range cases {
		spec, err := p.jobSpec(&job.Task{
			Name:             "test",
			Driver:           job.DriverContainer,
			Config:           rawBlock(t, "image = \"alpine:3.20\"\n"),
			WorkingDirectory: tc.dir,
		})
		if err != nil {
			t.Fatalf("jobSpec() = %v", err)
		}

		template := spec["template"].(map[string]any)["template"].(map[string]any)
		container := template["containers"].([]map[string]any)[0]
		if container["workingDir"] != tc.want {
			t.Errorf("workingDir = %v, want %v", container["workingDir"], tc.want)
		}
	}
}

// -------------------------------------------------------------------------
// THE SPEC
// -------------------------------------------------------------------------

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

// A task with no image cannot be translated, and fails as internal so it is not
// rerouted to a provider that fails the same way.
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

// An env block that does not evaluate fails the translation as internal; the
// job never runs with no environment at all.
func TestJobSpecRefusesAnUnreadableEnv(t *testing.T) {
	t.Parallel()

	_, p := newFakeGoogle(t)

	_, err := p.jobSpec(&job.Task{
		Name:   "test",
		Driver: job.DriverContainer,
		Config: rawBlock(t, "image = \"alpine:3.20\"\n"),
		Env:    rawBlock(t, "LIST = [\"a\", \"b\"]\n"),
	})

	if err == nil || plugin.Reroutable(err) {
		t.Fatalf("jobSpec() = %v, want an internal failure", err)
	}
}
