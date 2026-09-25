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
//
// Pools come in two layers: a provider's total, and a namespace's optional
// share of it. An execution charges both in one reservation and fits only if
// both have room.
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

// Total is the namespace a provider's own pools are counted under. No declared
// namespace can be empty, so it cannot collide with one.
const Total = ""

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Key identifies one counter: a pool of one provider in one period, under the
// provider's total or one namespace's share.
type Key struct {
	Namespace string
	Provider  string
	Pool      string
	Period    string
}

// Usage is what a set of counters holds, in base units.
type Usage map[Key]int64

// PoolRef names one pool of a reservation: the layer and the pool.
type PoolRef struct {
	Namespace string
	Pool      string
}

// Charge is one pool's share of a reservation.
type Charge struct {
	Namespace string
	Pool      string
	Period    string
	Amount    int64
	Limit     int64
}

// Reservation is what one execution asks of one provider's pools. Charges
// covers every pool of both layers, zero amounts included, so settling knows
// each period.
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
	Amounts  map[PoolRef]int64
}

// namespace returns the namespace whose share the reservation charged, or
// Total when it charged only the provider's pools.
func (h *Held) namespace() string {
	for ref := range h.Amounts {
		if ref.Namespace != Total {
			return ref.Namespace
		}
	}

	return Total
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

	// Settle replaces an execution's reservation with actual. An execution
	// with none settles to nothing.
	Settle(ctx context.Context, id execution.ID, actual map[PoolRef]int64) error

	// ReadUsage returns settled plus reserved usage in the given periods.
	ReadUsage(ctx context.Context, periods []string) (Usage, error)

	// Reap hands each reservation created before the cutoff to settle, and
	// settles those it returns true for. Returns how many were settled.
	Reap(ctx context.Context, before time.Time,
		settle func(context.Context, Held) (map[PoolRef]int64, bool)) (int, error)
}

// Ledger charges executions against every configured pool.
type Ledger struct {
	budgets quota.Budgets
	store   Store
	now     func() time.Time

	mu       sync.Mutex
	snapshot Usage
}

// Refusal is a reservation that would have taken a pool past its limit.
// Namespace is Total when the provider's own pool refused.
type Refusal struct {
	Namespace string
	Provider  string
	Pool      string
	Meter     quota.Meter
	Limit     int64
	Used      int64
	Needed    int64
}

// Error reports the pool, what is left, and what the task asked for, all in the
// unit the operator wrote the limit in.
func (r *Refusal) Error() string {
	owner := fmt.Sprintf("pool %q of provider %q", r.Pool, r.Provider)
	if r.Namespace != Total {
		owner = fmt.Sprintf("pool %q of namespace %q on provider %q", r.Pool, r.Namespace, r.Provider)
	}

	return fmt.Sprintf("%s has %g of %g %s left; this task needs %g",
		owner,
		r.Meter.Natural(r.Limit-r.Used), r.Meter.Natural(r.Limit), r.Meter.Unit(),
		r.Meter.Natural(r.Needed),
	)
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// New builds a ledger over every compiled pool and loads its first snapshot.
// Fails rather than starting empty, because an empty snapshot reads as nothing
// spent.
func New(ctx context.Context, budgets quota.Budgets, store Store) (*Ledger, error) {
	l := &Ledger{
		budgets:  budgets,
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

// Reserve charges e against the provider's pools and namespace's share of
// them, or against none.
//
// Refuses rather than reports. Admission checks quota earlier so that a plan
// can explain itself, but time passes between deciding and dispatching, and
// another process may have charged the pool in between.
func (l *Ledger) Reserve(
	ctx context.Context, id execution.ID, namespace, provider string, e quota.Execution,
) error {
	total, share := l.budgets.Total(provider), l.budgets.Share(namespace, provider)
	if total.Unlimited() && share.Unlimited() {
		return nil
	}

	now := l.now()

	r := Reservation{
		ID:       id,
		Provider: provider,
		CPU:      e.CPU,
		Memory:   e.Memory,
		Created:  now,
		Charges:  append(charges(Total, total, e, now), charges(namespace, share, e, now)...),
	}

	fits, standing, err := l.store.Reserve(ctx, &r)
	if err != nil {
		return fmt.Errorf("reserving quota: %w", err)
	}

	if !fits {
		return l.refusal(namespace, provider, standing, e, now)
	}

	_ = l.refresh(ctx)

	return nil
}

// Settle replaces a reservation with what the execution actually cost.
//
// A settle that could not be written leaves the reservation standing, which is
// the over-count, and the reaper settles it later.
func (l *Ledger) Settle(
	ctx context.Context, id execution.ID, namespace, provider string, actual quota.Execution,
) error {
	total, share := l.budgets.Total(provider), l.budgets.Share(namespace, provider)
	if total.Unlimited() && share.Unlimited() {
		return nil
	}

	if err := l.store.Settle(ctx, id, l.deltas(namespace, provider, actual)); err != nil {
		return fmt.Errorf("settling quota: %w", err)
	}

	_ = l.refresh(ctx)

	return nil
}

// Reap resolves reservations older than StaleAfter, which a process that died
// between reserving and settling leaves behind.
func (l *Ledger) Reap(ctx context.Context, resolve Resolver) (int, error) {
	before := l.now().Add(-StaleAfter)

	reaped, err := l.store.Reap(ctx, before, func(ctx context.Context, h Held) (map[PoolRef]int64, bool) {
		verdict, ran := resolve(ctx, h)

		switch verdict {
		case Drop:
			return nil, true
		case Stand:
			return h.Amounts, true
		case Finished:
			return l.deltas(h.namespace(), h.Provider, quota.Execution{
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

// deltas prices e against both layers.
func (l *Ledger) deltas(namespace, provider string, e quota.Execution) map[PoolRef]int64 {
	out := make(map[PoolRef]int64)

	for pool, amount := range l.budgets.Total(provider).Deltas(e) {
		out[PoolRef{Namespace: Total, Pool: pool}] = amount
	}

	if namespace != Total {
		for pool, amount := range l.budgets.Share(namespace, provider).Deltas(e) {
			out[PoolRef{Namespace: namespace, Pool: pool}] = amount
		}
	}

	return out
}

// refusal names the pool e did not fit, the provider's own first. The store
// refused against standing, so a pool with room there means the two disagree
// about the check.
func (l *Ledger) refusal(
	namespace, provider string, standing Usage, e quota.Execution, now time.Time,
) error {
	layers := []struct {
		namespace string
		limits    quota.Limits
	}{
		{Total, l.budgets.Total(provider)},
		{namespace, l.budgets.Share(namespace, provider)},
	}

	for _, layer := range layers {
		usage := usageOf(standing, layer.namespace, provider, layer.limits, now)

		if pool := layer.limits.Exceeded(usage, e); pool != nil {
			return &Refusal{
				Namespace: layer.namespace,
				Provider:  provider,
				Pool:      pool.Name,
				Meter:     pool.Meter,
				Limit:     pool.Limit,
				Used:      usage[pool.Name],
				Needed:    layer.limits.Deltas(e)[pool.Name],
			}
		}
	}

	return fmt.Errorf("provider %q refused a reservation its usage has room for", provider)
}

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// PoolUsage reports a provider's total usage and namespace's share of it from
// the last snapshot, keyed by pool name, for the pure decisions in quota.
func (l *Ledger) PoolUsage(namespace, provider string) (total, share quota.PoolUsage) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	total = usageOf(l.snapshot, Total, provider, l.budgets.Total(provider), now)
	share = usageOf(l.snapshot, namespace, provider, l.budgets.Share(namespace, provider), now)

	return total, share
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

// periods returns the distinct calendar keys every pool of both layers is
// currently counting under, sorted.
func (l *Ledger) periods() []string {
	now := l.now()
	seen := make(map[string]bool)
	periods := make([]string, 0, 2)

	add := func(limits quota.Limits) {
		for _, p := range limits.Pools() {
			key := p.Period.Key(now)

			if key == "" || seen[key] {
				continue
			}

			seen[key] = true
			periods = append(periods, key)
		}
	}

	for _, limits := range l.budgets.Totals {
		add(limits)
	}

	for _, shares := range l.budgets.Namespaces {
		for _, limits := range shares {
			add(limits)
		}
	}

	slices.Sort(periods)

	return periods
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// charges prices e against one layer's pools, zero amounts included.
func charges(namespace string, limits quota.Limits, e quota.Execution, now time.Time) []Charge {
	deltas := limits.Deltas(e)
	pools := limits.Pools()
	out := make([]Charge, 0, len(pools))

	for _, p := range pools {
		out = append(out, Charge{
			Namespace: namespace,
			Pool:      p.Name,
			Period:    p.Period.Key(now),
			Amount:    deltas[p.Name],
			Limit:     p.Limit,
		})
	}

	return out
}

// usageOf reads one layer's pools in their current periods out of usage.
func usageOf(usage Usage, namespace, provider string, limits quota.Limits, now time.Time) quota.PoolUsage {
	pools := limits.Pools()
	out := make(quota.PoolUsage, len(pools))

	for _, p := range pools {
		out[p.Name] = usage[Key{Namespace: namespace, Provider: provider, Pool: p.Name, Period: p.Period.Key(now)}]
	}

	return out
}
