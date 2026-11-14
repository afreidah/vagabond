// -------------------------------------------------------------------------------
// Lease Contract
//
// Author: Alex Freidah
//
// What a store promises about who may write a dispatch and its executions:
// only the holder of the current lease, owner and epoch, until the dispatch is
// done. A claim moves the lease, even back to the same owner.
// -------------------------------------------------------------------------------

package statetest

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// CONTRACT
// -------------------------------------------------------------------------

// LeaseContract runs every lease rule against a fresh store from newStore.
func LeaseContract(t *testing.T, newStore func(t *testing.T) Store) {
	for _, r := range leaseRules {
		t.Run(r.name, func(t *testing.T) { r.check(t, newStore(t)) })
	}
}

// leaseRule is one promise about leases, checked against a fresh store.
type leaseRule struct {
	name  string
	check func(t *testing.T, s Store)
}

// leaseRules are the promises LeaseContract checks.
var leaseRules = []leaseRule{
	{"the holder's writes land", holderWrites},
	{"writing the same record twice changes nothing", rewriteIsIdempotent},
	{"a claim refuses the old lease", claimRefusesOldLease},
	{"a claim by the same owner is a new lease", reclaimIsNewLease},
	{"a claim takes only lapsed, open, unskipped dispatches", claimTakesLapsed},
	{"the lease outlives the verdict until the dispatch is done", leaseOutlivesVerdict},
	{"a cancel request reaches the holder's renewal", cancelReachesRenewal},
	{"a write for an execution never created is refused", unknownIsRefused},
}

// -------------------------------------------------------------------------
// RULES
// -------------------------------------------------------------------------

// holderWrites: the holder creates, saves and appends output, and reads back
// what it saved.
func holderWrites(t *testing.T, s Store) {
	d := dispatched(t, s, held())
	r := recorded(t, s, d, time.Time{})

	r.ProviderID = "c-1"
	saveUnder(t, s, r, d.Lease())

	if got := get(t, s, r.ID); got.ProviderID != "c-1" || got.Node != "box-1" {
		t.Errorf("ProviderID, Node = %q, %q; want the saved and created ones", got.ProviderID, got.Node)
	}

	if err := s.AppendLog(t.Context(), r.ID, 0, []byte("out"), 0, d.Lease()); err != nil {
		t.Errorf("AppendLog() = %v", err)
	}
}

// rewriteIsIdempotent: a save repeated with the same record is accepted and
// leaves the record as it was.
func rewriteIsIdempotent(t *testing.T, s Store) {
	d := dispatched(t, s, held())
	r := recorded(t, s, d, time.Now().UTC().Truncate(time.Microsecond))

	saveUnder(t, s, r, d.Lease())
	first := get(t, s, r.ID)

	saveUnder(t, s, r, d.Lease())

	if diff := cmp.Diff(first, get(t, s, r.ID)); diff != "" {
		t.Errorf("the second save changed the record (-first +second):\n%s", diff)
	}
}

// claimRefusesOldLease: once another server claims the dispatch, every write
// under the old lease is refused and the new holder's land.
func claimRefusesOldLease(t *testing.T, s Store) {
	d := dispatched(t, s, lapsed())
	r := recorded(t, s, d, time.Time{})
	old := d.Lease()

	claimed := claim(t, s, claimer)
	if len(claimed) != 1 || claimed[0].Epoch != 2 || claimed[0].Owner != claimer {
		t.Fatalf("claimed %+v, want the dispatch under epoch 2", claimed)
	}

	refusedUnder(t, s, d, r, old)
	saveUnder(t, s, r, claimed[0].Lease())
}

// reclaimIsNewLease: a claim by the owner that held the dispatch still moves
// the lease, so its earlier holding is refused.
func reclaimIsNewLease(t *testing.T, s Store) {
	d := dispatched(t, s, lapsed())
	r := recorded(t, s, d, time.Time{})

	claimed := claim(t, s, owner)
	if len(claimed) != 1 || claimed[0].Epoch != 2 {
		t.Fatalf("claimed %+v, want the dispatch under epoch 2", claimed)
	}

	refusedUnder(t, s, d, r, d.Lease())
	saveUnder(t, s, r, claimed[0].Lease())
}

// claimTakesLapsed: a claim skips leases still running, dispatches in skip,
// and done dispatches.
func claimTakesLapsed(t *testing.T, s Store) {
	dispatched(t, s, held())
	skipped := dispatched(t, s, lapsed())
	closeUnder(t, s, dispatched(t, s, lapsed()))

	lapsedOne := dispatched(t, s, lapsed())

	claimed, err := s.ClaimDispatches(t.Context(), claimer, time.Now(), held(), []execution.ID{skipped.ID})
	if err != nil {
		t.Fatalf("ClaimDispatches() = %v", err)
	}

	if len(claimed) != 1 || claimed[0].ID != lapsedOne.ID {
		t.Errorf("claimed %+v, want only %s", claimed, lapsedOne.ID)
	}
}

// leaseOutlivesVerdict: after its verdict the holder still renews and writes,
// a second verdict is refused, and once done nothing is accepted.
func leaseOutlivesVerdict(t *testing.T, s Store) {
	d := dispatched(t, s, held())
	r := recorded(t, s, d, time.Time{})

	verdict := *d
	verdict.State, verdict.Ended = execution.DispatchSucceeded, time.Now().UTC().Truncate(time.Microsecond)

	if err := s.FinishDispatch(t.Context(), &verdict); err != nil {
		t.Fatalf("FinishDispatch() = %v", err)
	}

	if err := s.FinishDispatch(t.Context(), &verdict); !errors.Is(err, execution.ErrStale) {
		t.Errorf("a second FinishDispatch() = %v, want ErrStale", err)
	}

	if _, err := s.RenewDispatch(t.Context(), d.ID, d.Lease(), held()); err != nil {
		t.Errorf("RenewDispatch() after the verdict = %v", err)
	}

	saveUnder(t, s, r, d.Lease())
	closeUnder(t, s, d)

	refusedUnder(t, s, d, r, d.Lease())

	if got := getDispatch(t, s, d.ID); got.Done.IsZero() || got.State != execution.DispatchSucceeded {
		t.Errorf("dispatch = %+v, want succeeded and done", got)
	}
}

// cancelReachesRenewal: a cancel request is returned by the next renewal, the
// first request's time stands, and a done dispatch takes none.
func cancelReachesRenewal(t *testing.T, s Store) {
	d := dispatched(t, s, held())

	if requested, err := s.RenewDispatch(t.Context(), d.ID, d.Lease(), held()); err != nil || !requested.IsZero() {
		t.Fatalf("RenewDispatch() = %s, %v; want no request", requested, err)
	}

	first := time.Now().UTC().Truncate(time.Microsecond)

	for _, at := range []time.Time{first, first.Add(time.Minute)} {
		if err := s.RequestCancel(t.Context(), d.ID, at); err != nil {
			t.Fatalf("RequestCancel() = %v", err)
		}
	}

	if requested, err := s.RenewDispatch(t.Context(), d.ID, d.Lease(), held()); err != nil || !requested.Equal(first) {
		t.Errorf("RenewDispatch() = %s, %v; want the first request, %s", requested, err, first)
	}

	done := dispatched(t, s, held())
	closeUnder(t, s, done)

	if err := s.RequestCancel(t.Context(), done.ID, first); err != nil {
		t.Errorf("RequestCancel() of a done dispatch = %v", err)
	}

	if got := getDispatch(t, s, done.ID); !got.CancelRequested.IsZero() {
		t.Errorf("a done dispatch took a cancel request at %s", got.CancelRequested)
	}
}

// unknownIsRefused: a save or output write for an execution that was never
// created is refused as stale, even under the lease of the dispatch it names.
func unknownIsRefused(t *testing.T, s Store) {
	d := dispatched(t, s, held())
	r := &execution.Record{ID: newID(t), State: execution.StateRunning, Dispatch: d.ID, UpdatedAt: time.Now()}

	if err := s.Save(t.Context(), r, d.Lease()); !errors.Is(err, execution.ErrStale) {
		t.Errorf("Save() = %v, want ErrStale", err)
	}

	if err := s.AppendLog(t.Context(), r.ID, 0, []byte("out"), 0, d.Lease()); !errors.Is(err, execution.ErrStale) {
		t.Errorf("AppendLog() = %v, want ErrStale", err)
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// claim claims every lapsed dispatch for by, failing the test on an error.
func claim(t *testing.T, s Store, by string) []*execution.Dispatch {
	t.Helper()

	claimed, err := s.ClaimDispatches(t.Context(), by, time.Now(), held(), nil)
	if err != nil {
		t.Fatalf("ClaimDispatches() = %v", err)
	}

	return claimed
}

// closeUnder marks d done under its own lease, failing the test if that is
// refused.
func closeUnder(t *testing.T, s Store, d *execution.Dispatch) {
	t.Helper()

	if err := s.CloseDispatch(t.Context(), d.ID, d.Lease(), time.Now()); err != nil {
		t.Fatalf("CloseDispatch() = %v", err)
	}
}

// saveUnder saves r under lease, failing the test if it is refused.
func saveUnder(t *testing.T, s Store, r *execution.Record, lease execution.Lease) {
	t.Helper()

	if err := s.Save(t.Context(), r, lease); err != nil {
		t.Fatalf("Save() = %v", err)
	}
}

// refusedUnder checks that every write to d and r under lease is refused as
// stale.
func refusedUnder(t *testing.T, s Store, d *execution.Dispatch, r *execution.Record, lease execution.Lease) {
	t.Helper()

	if err := s.Save(t.Context(), r, lease); !errors.Is(err, execution.ErrStale) {
		t.Errorf("Save() = %v, want ErrStale", err)
	}

	if err := s.AppendLog(t.Context(), r.ID, 0, []byte("late"), 0, lease); !errors.Is(err, execution.ErrStale) {
		t.Errorf("AppendLog() = %v, want ErrStale", err)
	}

	if _, err := s.RenewDispatch(t.Context(), d.ID, lease, held()); !errors.Is(err, execution.ErrStale) {
		t.Errorf("RenewDispatch() = %v, want ErrStale", err)
	}

	if err := s.CloseDispatch(t.Context(), d.ID, lease, time.Now()); !errors.Is(err, execution.ErrStale) {
		t.Errorf("CloseDispatch() = %v, want ErrStale", err)
	}

	late := &execution.Record{ID: newID(t), State: execution.StatePending, Dispatch: d.ID, UpdatedAt: time.Now()}
	if err := s.Create(t.Context(), late, lease); !errors.Is(err, execution.ErrStale) {
		t.Errorf("Create() = %v, want ErrStale", err)
	}
}

// get reads the record for id, failing the test if it cannot.
func get(t *testing.T, s Store, id execution.ID) *execution.Record {
	t.Helper()

	r, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}

	return r
}

// getDispatch reads the dispatch for id, failing the test if it cannot.
func getDispatch(t *testing.T, s Store, id execution.ID) *execution.Dispatch {
	t.Helper()

	d, err := s.GetDispatch(t.Context(), id)
	if err != nil {
		t.Fatalf("GetDispatch() = %v", err)
	}

	return d
}
