// -------------------------------------------------------------------------------
// Usage Ledger
//
// Author: Alex Freidah
//
// The account of what has been charged against each provider's quota pools.
// Reserving, settling and reaping are decided by the store in one statement
// each, so two processes charging the same pool cannot both see room. This
// package turns executions into per-pool amounts and holds the snapshot
// admission reads between calls.
// -------------------------------------------------------------------------------

package ledger

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/quota"
)

// StaleAfter is how old a reservation must be before the reaper asks about it.
// It covers the gap between reserving and submitting, retry backoff included,
// so the reaper never asks a provider about a run that is about to exist.
const StaleAfter = time.Hour

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Key identifies one counter: a pool of one provider in one period.
type Key struct {
	Provider string
	Pool     string
	Period   string
}

// Usage is what a set of counters holds, in base units.
type Usage map[Key]int64

// Charge is one pool's share of a reservation.
type Charge struct {
	Pool   string
	Period string
	Amount int64
	Limit  int64
}

// Reservation is what one execution asks of one provider's pools. Charges
// covers every pool, zero amounts included, so settling knows each period.
type Reservation struct {
	ID       execution.ID
	Provider string
	CPU      int
	Memory   int
	Created  time.Time
	Charges  []Charge
}

// Held is an outstanding reservation as the reaper finds it.
type Held struct {
	ID       execution.ID
	Provider string
	CPU      int
	Memory   int
	Amounts  map[string]int64 // by pool
}

// Verdict is what became of a held execution.
type Verdict int

const (
	// Keep leaves the reservation: the run is still going, or nothing could
	// be learned about it.
	Keep Verdict = iota

	// Drop removes the reservation: the provider never ran it.
	Drop

	// Finished settles at what the run cost, from how long it ran.
	Finished

	// Stand settles at the reserved amounts: it ran, for an unknown time.
	Stand
)

// Resolver asks a provider what became of a held execution. The duration is
// read only for Finished.
type Resolver func(ctx context.Context, h Held) (Verdict, time.Duration)

// Store is where reservations and settled usage live.
type Store interface {
	// Reserve records r only if every charge fits. When it does not, the
	// usage it was refused against comes back so the refusal can name a
	// pool.
	Reserve(ctx context.Context, r *Reservation) (bool, Usage, error)

	// Settle replaces an execution's reservation with actual, by pool. An
	// execution with none settles to nothing.
	Settle(ctx context.Context, id execution.ID, actual map[string]int64) error

	// ReadUsage returns settled plus reserved usage in the given periods.
	ReadUsage(ctx context.Context, periods []string) (Usage, error)

	// Reap hands each reservation created before the cutoff to settle, and
	// settles those it returns true for. Returns how many were settled.
	Reap(ctx context.Context, before time.Time,
		settle func(context.Context, Held) (map[string]int64, bool)) (int, error)
}

// Ledger charges executions against every configured provider's pools.
type Ledger struct {
	limits map[string]quota.Limits
	store  Store
	now    func() time.Time

	mu       sync.Mutex
	snapshot Usage
}

// Refusal is a reservation that would have taken a pool past its limit.
type Refusal struct {
	Provider string
	Pool     string
	Meter    quota.Meter
	Limit    int64
	Used     int64
	Needed   int64
}

// Error reports the pool, what is left, and what the task asked for, all in the
// unit the operator wrote the limit in.
func (r *Refusal) Error() string {
	return fmt.Sprintf(
		"pool %q of provider %q has %g of %g %s left; this task needs %g",
		r.Pool, r.Provider,
		r.Meter.Natural(r.Limit-r.Used), r.Meter.Natural(r.Limit), r.Meter.Unit(),
		r.Meter.Natural(r.Needed),
	)
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// New builds a ledger over the compiled limits of every configured provider and
// loads its first snapshot. Fails rather than starting empty, because an empty
// snapshot reads as nothing spent.
func New(ctx context.Context, limits map[string]quota.Limits, store Store) (*Ledger, error) {
	l := &Ledger{
		limits:   limits,
		store:    store,
		now:      time.Now,
		snapshot: make(Usage),
	}

	if err := l.refresh(ctx); err != nil {
		return nil, err
	}

	return l, nil
}

// -------------------------------------------------------------------------
// RESERVE AND SETTLE
// -------------------------------------------------------------------------

// Reserve charges e against every pool its provider meters, or against none.
//
// Refuses rather than reports. Admission checks quota earlier so that a plan
// can explain itself, but time passes between deciding and dispatching, and
// another process may have charged the pool in between.
func (l *Ledger) Reserve(ctx context.Context, id execution.ID, provider string, e quota.Execution) error {
	limits, ok := l.limits[provider]
	if !ok || limits.Unlimited() {
		return nil
	}

	now := l.now()

	r := Reservation{
		ID:       id,
		Provider: provider,
		CPU:      e.CPU,
		Memory:   e.Memory,
		Created:  now,
		Charges:  charges(limits, e, now),
	}

	fits, standing, err := l.store.Reserve(ctx, &r)
	if err != nil {
		return fmt.Errorf("reserving quota: %w", err)
	}

	if !fits {
		return refusal(provider, limits, usageOf(standing, provider, limits, now), e)
	}

	_ = l.refresh(ctx)

	return nil
}

// Settle replaces a reservation with what the execution actually cost.
//
// A settle that could not be written leaves the reservation standing, which is
// the over-count, and the reaper settles it later.
func (l *Ledger) Settle(ctx context.Context, id execution.ID, provider string, actual quota.Execution) error {
	limits, ok := l.limits[provider]
	if !ok || limits.Unlimited() {
		return nil
	}

	if err := l.store.Settle(ctx, id, limits.Deltas(actual)); err != nil {
		return fmt.Errorf("settling quota: %w", err)
	}

	_ = l.refresh(ctx)

	return nil
}

// Reap resolves reservations older than StaleAfter, which a process that died
// between reserving and settling leaves behind.
func (l *Ledger) Reap(ctx context.Context, resolve Resolver) (int, error) {
	before := l.now().Add(-StaleAfter)

	reaped, err := l.store.Reap(ctx, before, func(ctx context.Context, h Held) (map[string]int64, bool) {
		verdict, ran := resolve(ctx, h)

		switch verdict {
		case Drop:
			return nil, true
		case Stand:
			return h.Amounts, true
		case Finished:
			return l.limits[h.Provider].Deltas(quota.Execution{
				CPU: h.CPU, Memory: h.Memory, Duration: ran,
			}), true
		default:
			return nil, false
		}
	})
	if err != nil {
		return reaped, fmt.Errorf("reaping quota reservations: %w", err)
	}

	_ = l.refresh(ctx)

	return reaped, nil
}

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// PoolUsage reports one provider's usage from the last snapshot, keyed by pool
// name, for the pure decisions in quota.
func (l *Ledger) PoolUsage(provider string) quota.PoolUsage {
	limits, ok := l.limits[provider]
	if !ok {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	return usageOf(l.snapshot, provider, limits, l.now())
}

// refresh replaces the snapshot with what the store holds now. A failed read
// keeps the last one.
func (l *Ledger) refresh(ctx context.Context) error {
	usage, err := l.store.ReadUsage(ctx, l.periods())
	if err != nil {
		return fmt.Errorf("reading quota usage: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.snapshot = usage

	return nil
}

// periods returns the distinct calendar keys every provider's pools are
// currently counting under, sorted.
func (l *Ledger) periods() []string {
	now := l.now()
	seen := make(map[string]bool)
	periods := make([]string, 0, 2)

	for _, limits := range l.limits {
		for _, p := range limits.Pools() {
			key := p.Period.Key(now)

			if key == "" || seen[key] {
				continue
			}

			seen[key] = true
			periods = append(periods, key)
		}
	}

	slices.Sort(periods)

	return periods
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// charges prices e against every pool, zero amounts included.
func charges(limits quota.Limits, e quota.Execution, now time.Time) []Charge {
	deltas := limits.Deltas(e)
	pools := limits.Pools()
	out := make([]Charge, 0, len(pools))

	for _, p := range pools {
		out = append(out, Charge{
			Pool:   p.Name,
			Period: p.Period.Key(now),
			Amount: deltas[p.Name],
			Limit:  p.Limit,
		})
	}

	return out
}

// usageOf reads one provider's pools in their current periods out of usage.
func usageOf(usage Usage, provider string, limits quota.Limits, now time.Time) quota.PoolUsage {
	pools := limits.Pools()
	out := make(quota.PoolUsage, len(pools))

	for _, p := range pools {
		out[p.Name] = usage[Key{Provider: provider, Pool: p.Name, Period: p.Period.Key(now)}]
	}

	return out
}

// refusal names the pool e did not fit. The store refused against usage, so a
// pool with room there means the two disagree about the check.
func refusal(provider string, limits quota.Limits, usage quota.PoolUsage, e quota.Execution) error {
	pool := limits.Exceeded(usage, e)
	if pool == nil {
		return fmt.Errorf("provider %q refused a reservation its usage has room for", provider)
	}

	return &Refusal{
		Provider: provider,
		Pool:     pool.Name,
		Meter:    pool.Meter,
		Limit:    pool.Limit,
		Used:     usage[pool.Name],
		Needed:   limits.Deltas(e)[pool.Name],
	}
}
