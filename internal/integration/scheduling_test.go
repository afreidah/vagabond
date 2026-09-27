//go:build integration

// -------------------------------------------------------------------------------
// Scheduling Against Real Usage
//
// Author: Alex Freidah
//
// What unit tests cannot express: ranking and admission over usage that is
// already in the store when the server starts.
// -------------------------------------------------------------------------------

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/cli"
)

// twoProviders are identical container providers with a 1000-second runtime
// pool each.
const twoProviders = `
provider "a" {
  type = "fake-container"

  pool "runtime" {
    meter  = "seconds"
    limit  = 1000
    period = "monthly"
  }
}

provider "b" {
  type = "fake-container"

  pool "runtime" {
    meter  = "seconds"
    limit  = 1000
    period = "monthly"
  }
}
`

// containerJob declares a task of the given timeout on either provider.
func containerJob(timeout string) string {
	return `
job "ci" {
  type = "batch"

  task "test" {
    driver  = "container"
    timeout = "` + timeout + `"

    config {
      image = "golang:1.27"
    }
  }
}
`
}

// The provider with more of its pool left ranks first, and the CLI selects
// what the API plans.
func TestPlan_RanksByHeadroom(t *testing.T) {
	h := newHarness(t, twoProviders)
	h.spend("a", "runtime", 900*second, 1000*second)
	client := h.serve()

	source := containerJob("1m")

	plan, err := client.Plan(context.Background(), "", api.PlanRequest{Source: source})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	task := plan.Tasks[0]
	if task.Selected != "b" || len(task.Candidates) != 2 || task.Candidates[1].Provider != "a" {
		t.Fatalf("plan = %+v, want b ahead of a", task)
	}

	if task.Candidates[0].Score <= task.Candidates[1].Score {
		t.Errorf("scores = %d, %d; want b's headroom to score higher", task.Candidates[0].Score, task.Candidates[1].Score)
	}

	code, stdout, stderr := run("job", "plan", writeJob(t, source))
	if code != cli.ExitSuccess || !strings.Contains(stdout, "Selected: b") {
		t.Errorf("job plan: exit %d\n%s%s", code, stdout, stderr)
	}
}

// A pool with less left than the task needs rejects it by name, and a run of it
// never ran.
func TestPlan_PartlySpentPoolRejects(t *testing.T) {
	const oneProvider = `
provider "a" {
  type = "fake-container"

  pool "runtime" {
    meter  = "seconds"
    limit  = 1000
    period = "monthly"
  }
}
`

	h := newHarness(t, oneProvider)
	h.spend("a", "runtime", 500*second, 1000*second)
	h.serve()

	path := writeJob(t, containerJob("10m"))

	code, stdout, _ := run("job", "plan", path)
	if code != cli.ExitFailure || !strings.Contains(stdout, "quota-exhausted") {
		t.Errorf("job plan: exit %d, want a quota rejection:\n%s", code, stdout)
	}

	code, _, stderr := run("job", "run", path)
	if code != cli.ExitNoCapacity {
		t.Errorf("job run: exit %d, want %d:\n%s", code, cli.ExitNoCapacity, stderr)
	}

	if got := h.used("a", "runtime"); got != 500*second {
		t.Errorf("runtime used = %d, want the seeded %d and nothing more", got, 500*second)
	}
}
