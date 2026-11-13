// -------------------------------------------------------------------------------
// Reaper Tests
//
// Author: Alex Freidah
//
// What becomes of a stale reservation: dropped when nothing was recorded on
// it, kept for its runner otherwise. The ledger's own tests cover what a drop
// does to the counters.
// -------------------------------------------------------------------------------

package dispatch

import (
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// A reservation with no execution record was never submitted on, so it is
// dropped; one with a record is its runner's to settle.
func TestResolve(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})
	d := newDispatcher(t, reg)
	o := begin(t, d)

	defer o.lease.release()

	recorded := &execution.Record{
		ID: newTestID(t), State: execution.StatePending, UpdatedAt: time.Now(), Dispatch: o.Dispatch, Provider: "a",
	}

	if err := reg.executions.Create(t.Context(), recorded, o.lease.key); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	for id, want := range map[execution.ID]bool{newTestID(t): true, recorded.ID: false} {
		if got := d.orphaned(t.Context(), id); got != want {
			t.Errorf("orphaned(%s) = %t, want %t", id, got, want)
		}
	}
}
