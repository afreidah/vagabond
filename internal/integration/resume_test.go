//go:build integration

// -------------------------------------------------------------------------------
// Resuming After a Restart
//
// Author: Alex Freidah
//
// A dispatch left in the store by a process that died mid-execution, as it
// would be left: running, its lease lapsed, its execution submitted to a
// provider that has since finished it, and its reservation still held. A server
// starting on that store claims it, follows the execution to its end, and
// settles it.
// -------------------------------------------------------------------------------

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/server"
)

// containerProvider has a runtime pool large enough never to refuse.
const containerProvider = `
provider "box" {
  type = "fake-container"

  pool "runtime" {
    meter  = "seconds"
    limit  = 100000
    period = "monthly"
  }
}
`

// reserved is what each abandoned execution holds: its declared hour.
const reserved = 3600 * second

// abandon seeds a dispatch a dead CLI left mid-execution, whose execution the
// provider has since finished, and returns its ID.
func (h *harness) abandon() execution.ID {
	h.t.Helper()

	ctx := context.Background()
	lapsed := time.Now().Add(-time.Hour)
	dispatchID, id := newID(h.t), newID(h.t)

	p, _ := h.registry.Provider("box")
	box := p.(*plugin.FakeContainerProvider)

	if _, err := box.Submit(ctx, id, &job.Task{Name: "wait"}); err != nil {
		h.t.Fatalf("Submit() = %v", err)
	}

	for _, next := range []execution.State{execution.StateRunning, execution.StateSucceeded} {
		if err := box.Advance(id, next); err != nil {
			h.t.Fatalf("Advance(%s) = %v", next, err)
		}
	}

	err := h.store.CreateDispatch(ctx, &execution.Dispatch{
		ID: dispatchID, Namespace: job.DefaultNamespace, Job: "sleepy", Tasks: 1,
		State: execution.DispatchRunning, Owner: "cli:dead:1", LeaseUntil: lapsed, Created: lapsed,
	})
	if err != nil {
		h.t.Fatalf("CreateDispatch() = %v", err)
	}

	fits, _, err := h.store.Reserve(ctx, &ledger.Reservation{
		ID: id, Provider: "box", CPU: 1000, Memory: 512, Created: time.Now(),
		Charges: []ledger.Charge{{
			Pool: "runtime", Period: quota.PeriodMonthly.Key(time.Now()), Amount: reserved, Limit: 100000 * second,
		}},
	})
	if err != nil || !fits {
		h.t.Fatalf("Reserve() = %t, %v", fits, err)
	}

	err = h.store.Create(ctx, &execution.Record{
		Status:    execution.Status{ID: id, State: execution.StateRunning, UpdatedAt: lapsed},
		Namespace: job.DefaultNamespace, Job: "sleepy", Dispatch: dispatchID, Task: "wait",
		Provider: "box", Attempt: 1, CPU: 1000, Memory: 512,
	})
	if err != nil {
		h.t.Fatalf("Create() = %v", err)
	}

	return dispatchID
}

// A server starting on the store resumes the abandoned dispatch, records it
// succeeded under its own lease, and settles the execution at what it ran
// rather than what it reserved.
func TestResume_AfterRestart(t *testing.T) {
	h := newHarness(t, containerProvider)
	id := h.abandon()
	client := h.serve(server.WithOwner("server-a"))

	if d := await(t, client, id.String()); d.State != "succeeded" {
		t.Fatalf("dispatch = %+v, want succeeded", d)
	}

	stored, err := h.store.GetDispatch(context.Background(), id)
	if err != nil || stored.Owner != "server-a" {
		t.Errorf("GetDispatch() = %+v, %v; want it owned by the server", stored, err)
	}

	// The fake reports one second.
	if got := h.used("box", "runtime"); got != second {
		t.Errorf("runtime used = %d, want the settled %d, not the reserved %d", got, second, reserved)
	}
}

// Two servers starting at once divide the abandoned dispatches between them:
// every one is resumed by one of them and settled once.
func TestResume_TwoServersClaimEachOnce(t *testing.T) {
	const abandoned = 6

	h := newHarness(t, containerProvider)

	ids := make([]execution.ID, 0, abandoned)
	for range abandoned {
		ids = append(ids, h.abandon())
	}

	client := h.serve(server.WithOwner("server-a"))
	h.serve(server.WithOwner("server-b"))

	for _, id := range ids {
		if d := await(t, client, id.String()); d.State != "succeeded" {
			t.Errorf("dispatch %s = %s, want succeeded", id, d.State)
		}

		stored, err := h.store.GetDispatch(context.Background(), id)
		if err != nil || (stored.Owner != "server-a" && stored.Owner != "server-b") {
			t.Errorf("GetDispatch(%s) = %+v, %v; want it owned by one of the servers", id, stored, err)
		}
	}

	if got := h.used("box", "runtime"); got != abandoned*second {
		t.Errorf("runtime used = %d, want each settled once: %d", got, abandoned*second)
	}
}
