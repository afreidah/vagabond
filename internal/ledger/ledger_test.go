// -------------------------------------------------------------------------------
// Ledger Tests
//
// Author: Alex Freidah
//
// Run against the memory store, which applies the same rules as Postgres. Two
// cases carry the design. A refused reservation must charge nothing at all, or a
// provider drifts upward every time a task is turned away. And a run that never
// settles must stay charged until the reaper learns what it did.
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

func fixtures() map[string]quota.Limits {
	return map[string]quota.Limits{
		"fn":        quota.FixtureFunction(),
		"container": quota.FixtureContainer(),
		"unlimited": {},
	}
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
		limits:   fixtures(),
		store:    store,
		now:      func() time.Time { return at },
		snapshot: make(Usage),
	}

	if err := l.refresh(t.Context()); err != nil {
		t.Fatalf("refresh() = %v", err)
	}

	return l
}

func newID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	return id
}

func reserve(t *testing.T, l *Ledger, id execution.ID, provider string, e quota.Execution) {
	t.Helper()

	if err := l.Reserve(t.Context(), id, provider, e); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
}

func settle(t *testing.T, l *Ledger, id execution.ID, provider string, actual quota.Execution) {
	t.Helper()

	if err := l.Settle(t.Context(), id, provider, actual); err != nil {
		t.Fatalf("Settle() = %v", err)
	}
}

var fnCompute = Key{Provider: "fn", Pool: "compute", Period: "2026-09"}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

var errStoreDown = errors.New("store down")

// unreadable is a store whose reads fail.
type unreadable struct{ *Memory }

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

func TestReserve_ChargesEveryPoolTheExecutionTouches(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)

	reserve(t, l, newID(t), "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})

	usage := l.PoolUsage("fn")

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

	err := l.Reserve(t.Context(), newID(t), "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})

	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Reserve() = %v, want *Refusal", err)
	}

	if refusal.Pool != "compute" {
		t.Errorf("Refusal.Pool = %q, want compute", refusal.Pool)
	}

	if got := l.PoolUsage("fn")["requests"]; got != 0 {
		t.Errorf("requests = %d after a refusal, want 0", got)
	}
}

// A pool the task does not charge cannot refuse it, so a spent compute budget
// must not block a task that declared no duration.
func TestReserve_IgnoresPoolsTheExecutionMisses(t *testing.T) {
	l, _ := newLedger(t, midSeptember, Usage{fnCompute: 400_000 * gbSeconds})

	reserve(t, l, newID(t), "fn", quota.Execution{Memory: 1024})
}

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

	err := second.Reserve(t.Context(), newID(t), "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})

	var refusal *Refusal
	if !errors.As(err, &refusal) {
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

	usage := l.PoolUsage("fn")

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

	if got := l.PoolUsage("fn")["compute"]; got != 0 {
		t.Fatalf("compute = %d before settling, want 0", got)
	}

	settle(t, l, id, "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})

	if got := l.PoolUsage("fn")["compute"]; got != 10*gbSeconds {
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

// Settling twice, or settling what the reaper already settled, charges once.
func TestSettle_IsIdempotent(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)
	id := newID(t)
	actual := quota.Execution{Memory: 1024, Duration: 10 * time.Second}

	reserve(t, l, id, "fn", quota.Execution{Memory: 1024, Duration: 15 * time.Minute})
	settle(t, l, id, "fn", actual)
	settle(t, l, id, "fn", actual)

	if got := l.PoolUsage("fn")["compute"]; got != 10*gbSeconds {
		t.Errorf("compute = %d after settling twice, want %d", got, 10*gbSeconds)
	}
}

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

	if got := l.PoolUsage("fn")["compute"]; got != want {
		t.Errorf("compute = %d, want %d", got, want)
	}
}

// -------------------------------------------------------------------------
// REAP
// -------------------------------------------------------------------------

// reaped reserves one execution an hour and a minute ago, then reaps with the
// given verdict.
func reaped(t *testing.T, verdict Verdict, ran time.Duration) (*Ledger, *Memory, int) {
	t.Helper()

	l, store := newLedger(t, midSeptember.Add(-StaleAfter-time.Minute), nil)

	reserve(t, l, newID(t), "container", quota.Execution{CPU: 1000, Memory: 512, Duration: 15 * time.Minute})

	l.now = func() time.Time { return midSeptember }

	n, err := l.Reap(t.Context(), func(context.Context, Held) (Verdict, time.Duration) {
		return verdict, ran
	})
	if err != nil {
		t.Fatalf("Reap() = %v", err)
	}

	return l, store, n
}

func TestReap_KeepLeavesTheReservation(t *testing.T) {
	l, store, n := reaped(t, Keep, 0)

	if n != 0 || len(store.held) != 1 {
		t.Errorf("Reap() settled %d, held %d; want 0 settled and the reservation kept", n, len(store.held))
	}

	if got := l.PoolUsage("container")["cpu"]; got != 900*cpuSeconds {
		t.Errorf("cpu = %d, want the reserved %d", got, 900*cpuSeconds)
	}
}

// Never ran, so nothing was spent, the execution count included.
func TestReap_DropChargesNothing(t *testing.T) {
	l, store, n := reaped(t, Drop, 0)

	if n != 1 || len(store.held) != 0 {
		t.Errorf("Reap() settled %d, held %d; want 1 and none", n, len(store.held))
	}

	for pool, used := range l.PoolUsage("container") {
		if used != 0 {
			t.Errorf("pool %q = %d after dropping, want 0", pool, used)
		}
	}
}

// Priced from the stored shape and how long the provider says it ran.
func TestReap_FinishedChargesWhatItRan(t *testing.T) {
	l, _, n := reaped(t, Finished, 10*time.Second)

	if n != 1 {
		t.Fatalf("Reap() settled %d, want 1", n)
	}

	if got := l.PoolUsage("container")["cpu"]; got != 10*cpuSeconds {
		t.Errorf("cpu = %d, want %d", got, 10*cpuSeconds)
	}
}

// Ran for an unknown time, so the estimate becomes the charge.
func TestReap_StandChargesTheReservation(t *testing.T) {
	l, store, n := reaped(t, Stand, 0)

	if n != 1 || len(store.held) != 0 {
		t.Errorf("Reap() settled %d, held %d; want 1 and none", n, len(store.held))
	}

	if got := l.PoolUsage("container")["cpu"]; got != 900*cpuSeconds {
		t.Errorf("cpu = %d, want the reserved %d", got, 900*cpuSeconds)
	}
}

// A reservation younger than StaleAfter may belong to a run about to be
// submitted, so the provider is not asked about it.
func TestReap_SkipsRecentReservations(t *testing.T) {
	l, _ := newLedger(t, midSeptember, nil)

	reserve(t, l, newID(t), "fn", quota.Execution{Memory: 1024, Duration: time.Minute})

	asked := false

	n, err := l.Reap(t.Context(), func(context.Context, Held) (Verdict, time.Duration) {
		asked = true

		return Drop, 0
	})
	if err != nil {
		t.Fatalf("Reap() = %v", err)
	}

	if asked || n != 0 {
		t.Errorf("Reap() asked about a fresh reservation (settled %d)", n)
	}
}

// -------------------------------------------------------------------------
// REPORTING
// -------------------------------------------------------------------------

// An operator reads back the unit they wrote the limit in, not the base unit
// the counters keep.
func TestRefusal_ErrorReportsNaturalUnits(t *testing.T) {
	l, _ := newLedger(t, midSeptember, Usage{fnCompute: 399_995 * gbSeconds})

	err := l.Reserve(t.Context(), newID(t), "fn", quota.Execution{Memory: 1024, Duration: 10 * time.Second})
	if err == nil {
		t.Fatal("Reserve() admitted an execution with no room")
	}

	for _, want := range []string{`pool "compute"`, "400000 GB-seconds", "needs 10"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Refusal.Error() = %q, want it to mention %q", err, want)
		}
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
	l.limits = map[string]quota.Limits{"fn": limits}

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

			if l.Reserve(t.Context(), id, "fn", quota.Execution{Memory: 128, Duration: time.Second}) == nil {
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

	if got := l.PoolUsage("fn")["requests"]; got != 5 {
		t.Errorf("requests = %d, want 5", got)
	}
}
