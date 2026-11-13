// -------------------------------------------------------------------------------
// Ledger Tests
//
// Author: Alex Freidah
//
// Run against the memory store, which applies the same rules as Postgres. A
// refused reservation charges nothing, and one never settled stays charged at
// its estimate unless the reaper finds no execution recorded for it.
// -------------------------------------------------------------------------------

package ledger

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// One GB-second and one vCPU-second in base units, for readable expectations.
const (
	gbSeconds  = 1024 * 1000
	cpuSeconds = 1000 * 1000
)

var (
	midSeptember = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	lastOfMonth  = time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC)
	firstOfNext  = time.Date(2026, 10, 1, 0, 1, 0, 0, time.UTC)
)

// ns is the namespace tests charge unless they are about namespaces. It
// declares no shares, so only provider totals apply.
const ns = "default"

// fixtures gives fn a total of 400,000 GB-seconds a month, and two namespaces
// a share of 300,000 each: together more than the total, so the total binds.
func fixtures() quota.Budgets {
	return quota.Budgets{
		Totals: map[string]quota.Limits{
			"fn":        quota.FixtureFunction(),
			"container": quota.FixtureContainer(),
			"unlimited": {},
		},
		Namespaces: map[string]map[string]quota.Limits{
			"ci":    {"fn": computeShare(300_000)},
			"batch": {"fn": computeShare(300_000)},
		},
	}
}

// computeShare is a monthly GB-seconds pool named compute, as the total's is.
func computeShare(limit int64) quota.Limits {
	limits, err := quota.NewLimits([]quota.PoolSpec{
		{Name: "compute", Meter: quota.MeterGBSeconds, Limit: limit, Period: quota.PeriodMonthly},
	})
	if err != nil {
		panic(err)
	}

	return limits
}

// total re-reads the store and returns a provider's total usage in ns.
func total(l *Ledger, provider string) quota.PoolUsage {
	got, _ := usage(l, ns, provider)

	return got
}

// usage re-reads the store and returns a provider's total and namespace's
// share, as admission would see them.
func usage(l *Ledger, namespace, provider string) (total, share quota.PoolUsage) {
	if err := l.Refresh(context.Background()); err != nil {
		panic(err)
	}

	return l.PoolUsage(namespace, provider)
}

// newLedger builds a ledger over the fixtures and a memory store holding used,
// with a fixed clock.
func newLedger(t *testing.T, at time.Time, used Usage) (*Ledger, *Memory) {
	t.Helper()

	store := NewMemory(used)

	return onStore(t, at, store), store
}

// onStore builds a ledger over an existing store, as a second process would.
func onStore(t *testing.T, at time.Time, store Store) *Ledger {
	t.Helper()

	l := &Ledger{
		budgets:  fixtures(),
		store:    store,
		now:      func() time.Time { return at },
		snapshot: make(Usage),
	}

	if err := l.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh() = %v", err)
	}

	return l
}

// newID mints an execution ID, failing the test if it cannot.
func newID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return id
}

// reserve charges e to provider in ns, failing the test on any error.
func reserve(t *testing.T, l *Ledger, id execution.ID, provider string, e quota.Execution) {
	t.Helper()

	if err := l.Reserve(t.Context(), id, ns, provider, e, false); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
}

// settle settles id at actual on provider in ns, failing the test on any error.
func settle(t *testing.T, l *Ledger, id execution.ID, provider string, actual quota.Execution) {
	t.Helper()

	if err := l.Settle(t.Context(), id, ns, provider, actual); err != nil {
		t.Fatalf("Settle() = %v", err)
	}
}

// fnCompute is fn's total compute counter for September.
var fnCompute = Key{Provider: "fn", Pool: "compute", Period: "2026-09"}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// errStoreDown is what an unreadable store fails with.
var errStoreDown = errors.New("store down")

// unreadable is a store whose reads fail.
type unreadable struct{ *Memory }

// ReadUsage fails with errStoreDown.
func (unreadable) ReadUsage(context.Context, []string) (Usage, error) {
	return nil, errStoreDown
}

// An empty snapshot reads as nothing spent, so one that could not load is an
// error rather than a fresh start.
func TestNew_FailsWhenTheStoreCannotBeRead(t *testing.T) {
	_, err := New(t.Context(), fixtures(), unreadable{NewMemory(nil)})
	if !errors.Is(err, errStoreDown) {
		t.Errorf("New() = %v, want the read failure", err)
	}
}

// -------------------------------------------------------------------------
// RESERVE
// -------------------------------------------------------------------------

// A reservation charges every pool the execution meters against.
func TestReserve_ChargesEveryPoolTheExecutionTouches(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)

	reserve(t, l, newID(t), "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})

	usage := total(l, "fn")

	if got := usage["requests"]; got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}

	if got := usage["compute"]; got != 10*gbSeconds {
		t.Errorf("compute = %d, want %d", got, 10*gbSeconds)
	}
}

// A refusal must leave every pool untouched, including the ones that had room.
func TestReserve_RefusedChargesNothing(t *testing.T) {
	l, _ := newLedger(t, midSeptember, Usage{fnCompute: 400_000 * gbSeconds})

	err := l.Reserve(t.Context(), newID(t), ns, "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second}, false)

	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Reserve() = %v, want *Refusal", err)
	}

	if refusal.Pool != "compute" {
		t.Errorf("Refusal.Pool = %q, want compute", refusal.Pool)
	}

	if got := total(l, "fn")["requests"]; got != 0 {
		t.Errorf("requests = %d after a refusal, want 0", got)
	}
}

// A job that pays is charged past a spent allowance rather than refused, and
// what it used counts against the pool like any other charge.
func TestReserve_PayingJobGoesPastTheLimit(t *testing.T) {
	spent := int64(400_000 * gbSeconds)
	l, _ := newLedger(t, midSeptember, Usage{fnCompute: spent})

	if err := l.Reserve(t.Context(), newID(t), ns, "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second}, true); err != nil {
		t.Fatalf("Reserve() for a paying job = %v, want it charged", err)
	}

	if got := total(l, "fn")["compute"]; got != spent+10*gbSeconds {
		t.Errorf("compute = %d, want the spent allowance plus the charge, %d", got, spent+10*gbSeconds)
	}
}

// A pool the task does not charge cannot refuse it, so a spent compute budget
// must not block a task that declared no duration.
func TestReserve_IgnoresPoolsTheExecutionMisses(t *testing.T) {
	l, _ := newLedger(t, midSeptember, Usage{fnCompute: 400_000 * gbSeconds})

	reserve(t, l, newID(t), "fn", quota.Execution{Memory: 1024})
}

// A provider with no budgets, or none configured at all, reserves without error.
func TestReserve_ProvidersWithNothingToEnforce(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)
	task := quota.Execution{Memory: 8192, Duration: time.Hour}

	for _, provider := range []string{"unlimited", "never-configured"} {
		t.Run(provider, func(t *testing.T) {
			reserve(t, l, newID(t), provider, task)
		})
	}
}

// The store decides, not the snapshot. A second process whose snapshot predates
// the first one's charge is still refused.
func TestReserve_RefusedByAChargeTheSnapshotMissed(t *testing.T) {
	store := NewMemory(Usage{fnCompute: 399_990 * gbSeconds})
	first := onStore(t, midSeptember, store)
	second := onStore(t, midSeptember, store)

	reserve(t, first, newID(t), "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})

	err := second.Reserve(t.Context(), newID(t), ns, "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second}, false)

	if _, ok := errors.AsType[*Refusal](err); !ok {
		t.Errorf("Reserve() = %v, want a refusal from the other ledger's charge", err)
	}
}

// -------------------------------------------------------------------------
// SETTLE
// -------------------------------------------------------------------------

// Reserving the declared timeout over-counts on purpose; settling is what gives
// the allowance back.
func TestSettle_CorrectsDownToActual(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)
	id := newID(t)

	reserve(t, l, id, "fn", quota.Execution{Memory: 1024, Duration: 15 * time.Minute})
	settle(t, l, id, "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})

	usage := total(l, "fn")

	if got := usage["compute"]; got != 10*gbSeconds {
		t.Errorf("compute = %d, want %d", got, 10*gbSeconds)
	}

	if got := usage["requests"]; got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

// A task's timeout is optional, so a run can reserve nothing against a
// time-metered pool and still cost something.
func TestSettle_ChargesAPoolTheReservationMissed(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)
	id := newID(t)

	reserve(t, l, id, "fn", quota.Execution{Memory: 1024})

	if got := total(l, "fn")["compute"]; got != 0 {
		t.Fatalf("compute = %d before settling, want 0", got)
	}

	settle(t, l, id, "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})

	if got := total(l, "fn")["compute"]; got != 10*gbSeconds {
		t.Errorf("compute = %d, want %d", got, 10*gbSeconds)
	}
}

// A run that spans a rollover settles where it was charged, so the correction
// cannot credit a period that never paid for it.
func TestSettle_ChargesThePeriodItWasReservedIn(t *testing.T) {
	l, store := newLedger(t, lastOfMonth, nil)
	id := newID(t)

	reserve(t, l, id, "fn", quota.Execution{Memory: 1024, Duration: 15 * time.Minute})

	l.now = func() time.Time { return firstOfNext }
	settle(t, l, id, "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})

	if got := store.used[fnCompute]; got != 10*gbSeconds {
		t.Errorf("september compute = %d, want %d", got, 10*gbSeconds)
	}

	october := Key{Provider: "fn", Pool: "compute", Period: "2026-10"}
	if got, ok := store.used[october]; ok {
		t.Errorf("october compute = %d, want no entry at all", got)
	}
}

// Settling twice, as a runner restored after its settle may, charges once.
func TestSettle_IsIdempotent(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)
	id := newID(t)
	actual := quota.Execution{Memory: 1024, Duration: 10 * time.Second}

	reserve(t, l, id, "fn", quota.Execution{Memory: 1024, Duration: 15 * time.Minute})
	settle(t, l, id, "fn", actual)
	settle(t, l, id, "fn", actual)

	if got := total(l, "fn")["compute"]; got != 10*gbSeconds {
		t.Errorf("compute = %d after settling twice, want %d", got, 10*gbSeconds)
	}
}

// Settling an execution with no reservation writes nothing.
func TestSettle_UnknownExecutionIsNoOp(t *testing.T) {
	l, store := newLedger(t, midSeptember, nil)

	settle(t, l, newID(t), "fn", quota.Execution{Memory: 1024, Duration: time.Hour})

	if got := len(store.used); got != 0 {
		t.Errorf("store holds %d counters after settling nothing", got)
	}
}

// The deliberate over-count: a run whose outcome is unknown keeps the estimate
// it reserved.
func TestUnsettledExecutionStaysCharged(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)

	reserve(t, l, newID(t), "fn", quota.Execution{Memory: 1024, Duration: 15 * time.Minute})

	want := int64(900 * gbSeconds) // 15 minutes at one gibibyte

	if got := total(l, "fn")["compute"]; got != want {
		t.Errorf("compute = %d, want %d", got, want)
	}
}

// -------------------------------------------------------------------------
// NAMESPACES
// -------------------------------------------------------------------------

// tenGBSeconds is an execution charging ten GB-seconds of compute.
var tenGBSeconds = quota.Execution{Memory: 1024, Duration: 10 * time.Second}

// A namespace's share is charged alongside the provider's total.
func TestReserve_ChargesTheTotalAndTheShare(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)

	if err := l.Reserve(t.Context(), newID(t), "ci", "fn", tenGBSeconds, false); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}

	totalUsage, share := usage(l, "ci", "fn")

	if totalUsage["compute"] != 10*gbSeconds || share["compute"] != 10*gbSeconds {
		t.Errorf("total = %v, share = %v; want 10 GB-seconds in both", totalUsage, share)
	}

	if _, other := l.PoolUsage("batch", "fn"); other["compute"] != 0 {
		t.Errorf("batch share = %v, charged for ci's run", other)
	}
}

// Shares are independent: one namespace spending its own does not refuse
// another.
func TestReserve_ShareRefusesItsOwnNamespaceOnly(t *testing.T) {
	ciCompute := Key{Namespace: "ci", Provider: "fn", Pool: "compute", Period: "2026-09"}
	l, _ := newLedger(t, midSeptember, Usage{ciCompute: 299_995 * gbSeconds})

	err := l.Reserve(t.Context(), newID(t), "ci", "fn", tenGBSeconds, false)

	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Namespace != "ci" {
		t.Fatalf("Reserve() = %v, want a refusal from ci's share", err)
	}

	if !strings.Contains(err.Error(), `namespace "ci"`) {
		t.Errorf("Refusal.Error() = %q, want it to name the namespace", err)
	}

	if err := l.Reserve(t.Context(), newID(t), "batch", "fn", tenGBSeconds, false); err != nil {
		t.Errorf("Reserve() in batch = %v, want it to fit", err)
	}
}

// Shares add up past the total, so the total still binds with a share to
// spare.
func TestReserve_TotalBindsAcrossShares(t *testing.T) {
	l, _ := newLedger(t, midSeptember, Usage{fnCompute: 399_995 * gbSeconds})

	err := l.Reserve(t.Context(), newID(t), "ci", "fn", tenGBSeconds, false)

	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Namespace != Total {
		t.Errorf("Reserve() = %v, want a refusal from the provider's total", err)
	}
}

// Settling corrects both layers.
func TestSettle_CorrectsTheShareToo(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)
	id := newID(t)

	if err := l.Reserve(t.Context(), id, "ci", "fn", quota.Execution{Memory: 1024, Duration: 15 * time.Minute}, false); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}

	if err := l.Settle(t.Context(), id, "ci", "fn", tenGBSeconds); err != nil {
		t.Fatalf("Settle() = %v", err)
	}

	totalUsage, share := usage(l, "ci", "fn")

	if totalUsage["compute"] != 10*gbSeconds || share["compute"] != 10*gbSeconds {
		t.Errorf("total = %v, share = %v; want 10 GB-seconds in both", totalUsage, share)
	}
}

// -------------------------------------------------------------------------
// DROP
// -------------------------------------------------------------------------

// unwritable is a store whose settlements fail.
type unwritable struct{ *Memory }

// Settle fails with errStoreDown.
func (unwritable) Settle(context.Context, execution.ID, map[PoolRef]int64) error {
	return errStoreDown
}

// A refused submission ran nothing, so dropping it gives back every pool, the
// execution count included.
func TestDrop_ChargesNothing(t *testing.T) {
	l, store := newLedger(t, midSeptember, nil)
	id := newID(t)

	reserve(t, l, id, "container", quota.Execution{CPU: 1000, Memory: 512, Duration: 15 * time.Minute})

	if err := l.Drop(t.Context(), id); err != nil {
		t.Fatalf("Drop() = %v", err)
	}

	if len(store.held) != 0 {
		t.Errorf("store holds %d reservations after dropping, want none", len(store.held))
	}

	for pool, used := range total(l, "container") {
		if used != 0 {
			t.Errorf("pool %q = %d after dropping, want 0", pool, used)
		}
	}
}

// A drop that could not be written says so, and the reservation stays for the
// runner to drop again.
func TestDrop_FailsWhenTheStoreCannotWrite(t *testing.T) {
	l, err := New(t.Context(), fixtures(), unwritable{NewMemory(nil)})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	if err := l.Drop(t.Context(), newID(t)); !errors.Is(err, errStoreDown) {
		t.Errorf("Drop() = %v, want the write failure", err)
	}
}

// -------------------------------------------------------------------------
// REAP
// -------------------------------------------------------------------------

// reaped reserves one execution an hour and a minute ago, then reaps with
// orphaned answering as given.
func reaped(t *testing.T, orphaned bool) (*Ledger, *Memory, int) {
	t.Helper()

	l, store := newLedger(t, midSeptember.Add(-StaleAfter-time.Minute), nil)

	reserve(t, l, newID(t), "container", quota.Execution{CPU: 1000, Memory: 512, Duration: 15 * time.Minute})

	l.now = func() time.Time { return midSeptember }

	n, err := l.Reap(t.Context(), func(context.Context, execution.ID) bool { return orphaned })
	if err != nil {
		t.Fatalf("Reap() = %v", err)
	}

	return l, store, n
}

// A recorded execution's reservation is its runner's to settle, so it stays
// held and charged at its estimate.
func TestReap_KeepsARecordedExecution(t *testing.T) {
	l, store, n := reaped(t, false)

	if n != 0 || len(store.held) != 1 {
		t.Errorf("Reap() dropped %d, held %d; want none dropped and the reservation kept", n, len(store.held))
	}

	if got := total(l, "container")["cpu"]; got != 900*cpuSeconds {
		t.Errorf("cpu = %d, want the reserved %d", got, 900*cpuSeconds)
	}
}

// An execution never recorded was never submitted, so nothing was spent, the
// execution count included.
func TestReap_DropsAnOrphanAtNothing(t *testing.T) {
	l, store, n := reaped(t, true)

	if n != 1 || len(store.held) != 0 {
		t.Errorf("Reap() dropped %d, held %d; want 1 and none", n, len(store.held))
	}

	for pool, used := range total(l, "container") {
		if used != 0 {
			t.Errorf("pool %q = %d after dropping, want 0", pool, used)
		}
	}
}

// A reservation younger than StaleAfter may belong to an execution about to
// be recorded, so it is not looked at.
func TestReap_SkipsRecentReservations(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)

	reserve(t, l, newID(t), "fn", quota.Execution{Memory: 1024, Duration: time.Minute})

	asked := false

	n, err := l.Reap(t.Context(), func(context.Context, execution.ID) bool {
		asked = true

		return true
	})
	if err != nil {
		t.Fatalf("Reap() = %v", err)
	}

	if asked || n != 0 {
		t.Errorf("Reap() asked about a fresh reservation (dropped %d)", n)
	}
}

// -------------------------------------------------------------------------
// REPORTING
// -------------------------------------------------------------------------

// An operator reads back the unit they wrote the limit in, not the base unit
// the counters keep.
func TestRefusal_ErrorReportsNaturalUnits(t *testing.T) {
	l, _ := newLedger(t, midSeptember, Usage{fnCompute: 399_995 * gbSeconds})

	err := l.Reserve(t.Context(), newID(t), ns, "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second}, false)
	if err == nil {
		t.Fatal("Reserve() admitted an execution with no room")
	}

	for _, want := range []string{`pool "compute"`, "400000 GB-seconds", "needs 10"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Refusal.Error() = %q, want it to mention %q", err, want)
		}
	}
}

// A paying job can take a pool past its limit, and what is left then is nothing
// rather than a negative amount.
func TestRefusal_ErrorFloorsWhatIsLeftAtZero(t *testing.T) {
	l, _ := newLedger(t, midSeptember, Usage{fnCompute: 400_100 * gbSeconds})

	err := l.Reserve(t.Context(), newID(t), ns, "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second}, false)
	if err == nil {
		t.Fatal("Reserve() admitted an execution past the limit")
	}

	if want := "has 0 of 400000"; !strings.Contains(err.Error(), want) {
		t.Errorf("Refusal.Error() = %q, want it to mention %q", err, want)
	}
}

// -------------------------------------------------------------------------
// CONCURRENCY
// -------------------------------------------------------------------------

// Reserving is the only thing that has to be exact. A pool with room for five
// admits five, whatever arrives at once.
func TestReserve_ConcurrentReservationsStopAtTheLimit(t *testing.T) {
	limits, err := quota.NewLimits([]quota.PoolSpec{
		{Name: "requests", Meter: quota.MeterExecutions, Limit: 5, Period: quota.PeriodMonthly},
	})
	if err != nil {
		t.Fatalf("NewLimits() = %v", err)
	}

	l, _ := newLedger(t, midSeptember, nil)
	l.budgets = quota.Budgets{Totals: map[string]quota.Limits{"fn": limits}}

	const attempts = 50

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
	)

	for range attempts {
		wg.Go(func() {
			id, err := execution.NewID()
			if err != nil {
				return
			}

			if l.Reserve(t.Context(), id, ns, "fn", quota.Execution{Memory: 128, Duration: time.Second}, false) == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	if admitted != 5 {
		t.Errorf("admitted %d of %d attempts, want 5", admitted, attempts)
	}

	if got := total(l, "fn")["requests"]; got != 5 {
		t.Errorf("requests = %d, want 5", got)
	}
}
