//go:build integration

// -------------------------------------------------------------------------------
// Dispatch Ownership
//
// Author: Alex Freidah
//
// Who acts on a dispatch when its owner cannot. A cancel asked of a server
// that does not hold the dispatch waits in the store for whichever server
// claims it. A server cut off from the store past its lease loses the
// dispatch to another, and when the store returns its writes are refused, so
// the run is ended, released and charged by the one that claimed it.
// -------------------------------------------------------------------------------

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/server"
	"github.com/afreidah/vagabond/internal/state/postgres/pgtest"
)

// -------------------------------------------------------------------------
// CANCEL
// -------------------------------------------------------------------------

// A cancel of an execution whose dispatch is still leased to a dead CLI is
// recorded, and the server that claims the dispatch once the lease lapses
// cancels the execution and charges what it ran.
func TestCancel_ReachesADispatchNoServerHolds(t *testing.T) {
	ctx := context.Background()

	h := newHarness(t, containerProvider)
	dispatch, id := h.leave(time.Now().Add(outageLease), execution.StateRunning)
	client := h.serve(server.WithOwner("server-a"), server.WithLeases(outageLease, outageRenew, outageClaim))

	if exec, err := client.Cancel(ctx, id.String()); err != nil || exec.State != string(execution.StateRunning) {
		t.Fatalf("Cancel() = %+v, %v; want it accepted while still running", exec, err)
	}

	if d := await(t, client, dispatch.String()); d.State != string(execution.DispatchUnanswered) {
		t.Fatalf("dispatch = %+v, want unanswered once cancelled", d)
	}

	if status, err := h.box().Status(ctx, id); err != nil || status.State != execution.StateCancelled {
		t.Errorf("provider status = %+v, %v; want cancelled", status, err)
	}

	stored, err := h.store.GetDispatch(ctx, dispatch)
	if err != nil || stored.Owner != "server-a" || stored.Epoch != 2 || stored.Done.IsZero() {
		t.Errorf("GetDispatch() = %+v, %v; want done under the claiming server", stored, err)
	}

	if got := h.used("box", "runtime"); got <= 0 || got >= reserved {
		t.Errorf("runtime used = %d, want what it ran, under the reserved %d", got, reserved)
	}
}

// -------------------------------------------------------------------------
// LOST LEASE
// -------------------------------------------------------------------------

// A server cut off from the store past its lease loses the dispatch to a
// second server, which follows the run to its end. When the store returns,
// the first server's writes are refused, and the record, the dispatch and the
// charge stay as the second left them.
func TestLease_CutOffOwnerStopsWhenTheStoreReturns(t *testing.T) {
	ctx := context.Background()

	proxy := pgtest.NewProxy(t, pgtest.Postgres())
	cut := &harness{t: t, registry: providers(t, containerProvider), store: proxy.Open(t, pgtest.Postgres())}
	other := &harness{t: t, registry: cut.registry, store: pgtest.Open(t, pgtest.Postgres())}
	leases := server.WithLeases(outageLease, outageRenew, outageClaim)

	client := cut.serve(server.WithOwner("server-a"), leases)

	started, err := client.Run(ctx, "", sleepyJob, nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	id := submitted(t, client, started.DispatchID)

	proxy.Cut()

	claimer := other.serve(server.WithOwner("server-b"), leases)

	for _, next := range []execution.State{execution.StateRunning, execution.StateSucceeded} {
		if err := other.box().Advance(id, next); err != nil {
			t.Fatalf("Advance(%s) = %v", next, err)
		}
	}

	if d := await(t, claimer, started.DispatchID); d.State != string(execution.DispatchSucceeded) {
		t.Fatalf("dispatch = %+v, want succeeded under the second server", d)
	}

	proxy.Restore(t)

	// Long enough for the first server's renewal and its pending write to be refused
	time.Sleep(3 * outageRenew)

	dispatch, err := execution.ParseID(started.DispatchID)
	if err != nil {
		t.Fatalf("ParseID() = %v", err)
	}

	stored, err := other.store.GetDispatch(ctx, dispatch)
	if err != nil || stored.Owner != "server-b" || stored.Epoch != 2 || stored.State != execution.DispatchSucceeded {
		t.Errorf("GetDispatch() = %+v, %v; want succeeded under the second server", stored, err)
	}

	rec, err := other.store.Get(ctx, id)
	if err != nil || rec.State != execution.StateSucceeded || rec.Result == nil || rec.Released.IsZero() {
		t.Errorf("Get() = %+v, %v; want succeeded with its result, released", rec, err)
	}

	// The fake reports one second.
	if got := other.used("box", "runtime"); got != second {
		t.Errorf("runtime used = %d, want the settled %d", got, second)
	}
}
