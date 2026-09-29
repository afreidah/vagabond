//go:build integration

// -------------------------------------------------------------------------------
// Store Outage
//
// Author: Alex Freidah
//
// The store cut out from under a running server, through a proxy the test
// controls. New work is refused, plans still answer, and a run already in
// flight finishes on its provider. What the outage kept from being written is
// repaired once the store returns, by the server claiming its own lapsed
// dispatch and resuming it.
// -------------------------------------------------------------------------------

package integration

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/server"
	"github.com/afreidah/vagabond/internal/state/postgres/pgtest"
)

// sleepyJob runs on the container provider until the test advances it.
const sleepyJob = `
job "sleepy" {
  type = "batch"

  task "wait" {
    driver  = "container"
    timeout = "1h"

    config {
      image = "alpine:3.20"
    }
  }
}
`

// Short leases, so the outage's lapsed lease is claimed within seconds.
const (
	outageLease = 2 * time.Second
	outageRenew = 500 * time.Millisecond
	outageClaim = 500 * time.Millisecond
)

// A store outage refuses new dispatches and reports degraded health, while
// plans answer and the run in flight finishes. When the store returns, that
// run's dispatch ends succeeded and its quota settles.
func TestStoreOutage_RefusesNewWorkAndRecovers(t *testing.T) {
	ctx := context.Background()

	proxy := pgtest.NewProxy(t, pgtest.Postgres())
	h := &harness{t: t, registry: providers(t, containerProvider), store: proxy.Open(t, pgtest.Postgres())}
	client := h.serve(server.WithLeases(outageLease, outageRenew, outageClaim))

	started, err := client.Run(ctx, "", sleepyJob, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	id := submitted(t, client, started.DispatchID)

	proxy.Cut()

	var failure *api.ResponseError

	if _, err := client.Run(ctx, "", sleepyJob, nil); !errors.As(err, &failure) || failure.Status != http.StatusServiceUnavailable {
		t.Errorf("Run() during the outage = %v, want 503", err)
	}

	// Reads are as unavailable as writes, not internal errors.
	reads := map[string]func() error{
		"DispatchStatus": func() error { _, err := client.DispatchStatus(ctx, started.DispatchID); return err },
		"Execution":      func() error { _, err := client.Execution(ctx, id.String()); return err },
		"Jobs":           func() error { _, err := client.Jobs(ctx, ""); return err },
	}

	for name, read := range reads {
		if err := read(); !errors.As(err, &failure) || failure.Status != http.StatusServiceUnavailable {
			t.Errorf("%s() during the outage = %v, want 503", name, err)
		}
	}

	if health, err := client.Health(ctx); err == nil || health.Store != "unreachable" {
		t.Errorf("Health() during the outage = %+v, %v; want unreachable", health, err)
	}

	if _, err := client.Plan(ctx, "", api.PlanRequest{Source: sleepyJob}); err != nil {
		t.Errorf("Plan() during the outage = %v, want an answer from the snapshot", err)
	}

	p, _ := h.registry.Provider("box")
	box := p.(*plugin.FakeContainerProvider)

	for _, next := range []execution.State{execution.StateRunning, execution.StateSucceeded} {
		if err := box.Advance(id, next); err != nil {
			t.Fatalf("Advance(%s) = %v", next, err)
		}
	}

	// Long enough for the next poll to see it finish and fail to record it.
	time.Sleep(3 * time.Second)

	proxy.Restore(t)

	if d := await(t, client, started.DispatchID); d.State != "succeeded" {
		t.Fatalf("dispatch after the outage = %+v, want succeeded", d)
	}

	if health, err := client.Health(ctx); err != nil || health.Store != "ok" {
		t.Errorf("Health() after the outage = %+v, %v; want ok", health, err)
	}

	// The fake reports one second.
	if got := h.used("box", "runtime"); got != second {
		t.Errorf("runtime used = %d, want the settled %d", got, second)
	}
}

// submitted waits for a dispatch's first execution to be submitted and returns
// its ID.
func submitted(t *testing.T, client *api.Client, dispatch string) execution.ID {
	t.Helper()

	deadline := time.Now().Add(runTimeout)

	for time.Now().Before(deadline) {
		d, err := client.DispatchStatus(context.Background(), dispatch)
		if err != nil {
			t.Fatalf("DispatchStatus() = %v", err)
		}

		if len(d.Executions) > 0 && d.Executions[0].State != string(execution.StatePending) {
			id, err := execution.ParseID(d.Executions[0].ID)
			if err != nil {
				t.Fatalf("ParseID() = %v", err)
			}

			return id
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("dispatch %s submitted nothing within %s", dispatch, runTimeout)

	return execution.ID{}
}
