//go:build integration

// -------------------------------------------------------------------------------
// Runs End to End
//
// Author: Alex Freidah
//
// A job submitted through the CLI, run on a provider, settled in the store, and
// read back over the API; and the registered-job lifecycle around it. The
// function provider finishes inside Submit with whatever exit code the test
// sets.
// -------------------------------------------------------------------------------

package integration

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/cli"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
)

// functionProvider has a request pool and a compute pool, as Lambda does.
const functionProvider = `
provider "fn" {
  type = "fake-function"

  pool "requests" {
    meter  = "executions"
    limit  = 1000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 400000
    period = "monthly"
  }
}
`

// functionJob is one function task with a declared shape.
const functionJob = `
job "hello" {
  type = "batch"

  task "greet" {
    driver = "function"

    config {
      function = "hello"
    }

    resources {
      memory = 1024
    }
  }
}
`

// dispatchID finds the dispatch ID job run and job dispatch print first.
var dispatchID = regexp.MustCompile(`==> dispatch ([0-9a-f-]{36})`)

// exitWith makes the harness's function provider exit with code.
func (h *harness) exitWith(code int) {
	p, _ := h.registry.Provider("fn")
	p.(*plugin.FakeSyncProvider).ExitCode = new(code)
}

// A job run through the CLI exits with the task's result, and the run, its
// execution, and its charge read back as they happened.
func TestRun_EndToEnd(t *testing.T) {
	h := newHarness(t, functionProvider)
	client := h.serve()

	code, _, stderr := run("job", "run", writeJob(t, functionJob))
	if code != cli.ExitSuccess {
		t.Fatalf("job run: exit %d\n%s", code, stderr)
	}

	match := dispatchID.FindStringSubmatch(stderr)
	if match == nil {
		t.Fatalf("no dispatch ID in:\n%s", stderr)
	}

	d := await(t, client, match[1])
	if d.State != "succeeded" || len(d.Executions) != 1 {
		t.Fatalf("dispatch = %+v, want succeeded with one execution", d)
	}

	e, err := client.Execution(context.Background(), d.Executions[0].ID)
	if err != nil || e.State != "succeeded" || e.ExitCode == nil || *e.ExitCode != 0 {
		t.Errorf("execution = %+v, %v", e, err)
	}

	if _, err := client.Logs(context.Background(), e.ID); err != nil {
		t.Errorf("Logs() = %v", err)
	}

	// Settled at the one second the fake reports, not the reserved worst case.
	settled := h.registry.Budgets().Total("fn").Deltas(quota.Execution{Memory: 1024, Duration: time.Second})["compute"]
	if got := h.used("fn", "compute"); got != settled || h.used("fn", "requests") != 1 {
		t.Errorf("compute = %d, requests = %d; want %d and 1", got, h.used("fn", "requests"), settled)
	}
}

// A task that exits non-zero fails the run with exit 1, and the failure is
// recorded as an answer, not an outage.
func TestRun_TaskFailure(t *testing.T) {
	h := newHarness(t, functionProvider)
	h.exitWith(3)
	h.serve()

	code, _, stderr := run("job", "run", writeJob(t, functionJob))
	if code != cli.ExitFailure || !strings.Contains(stderr, "failed (exit 3)") {
		t.Errorf("job run: exit %d, want %d:\n%s", code, cli.ExitFailure, stderr)
	}
}

// Register, dispatch by name, read status, stop, and a stopped job refused.
func TestRegisteredJob_Lifecycle(t *testing.T) {
	h := newHarness(t, functionProvider)
	client := h.serve()

	path := writeJob(t, functionJob)

	if code, stdout, _ := run("job", "register", path); code != cli.ExitSuccess || !strings.Contains(stdout, "version 1") {
		t.Fatalf("job register: exit %d\n%s", code, stdout)
	}

	if _, stdout, _ := run("job", "register", path); !strings.Contains(stdout, "unchanged at version 1") {
		t.Errorf("second register:\n%s", stdout)
	}

	if code, _, stderr := run("job", "dispatch", "hello"); code != cli.ExitSuccess {
		t.Fatalf("job dispatch: exit %d\n%s", code, stderr)
	}

	status, err := client.JobStatus(context.Background(), "", "hello")
	if err != nil || len(status.Executions) != 1 || status.Executions[0].JobVersion != 1 {
		t.Fatalf("JobStatus() = %+v, %v; want one execution of version 1", status, err)
	}

	if code, _, stderr := run("job", "stop", "hello"); code != cli.ExitSuccess {
		t.Fatalf("job stop: exit %d\n%s", code, stderr)
	}

	code, _, stderr := run("job", "dispatch", "hello")
	if code != cli.ExitFailure || !strings.Contains(stderr, "stopped") {
		t.Errorf("dispatch of a stopped job: exit %d\n%s", code, stderr)
	}

	var failure *api.ResponseError

	_, err = client.Dispatch(context.Background(), "", "hello", nil)
	if !errors.As(err, &failure) || failure.Status != http.StatusConflict {
		t.Errorf("Dispatch() = %v, want 409", err)
	}
}
