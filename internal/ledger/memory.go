// -------------------------------------------------------------------------------
// Memory Store
//
// Author: Alex Freidah
//
// The store with no database behind it, for a deployment that declared none and
// for tests. The same rules as the Postgres store, applied under one mutex
// instead of a serializable transaction. Everything is lost with the process.
// -------------------------------------------------------------------------------

package ledger

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// Memory holds settled usage and open reservations in maps.
type Memory struct {
	mu   sync.Mutex
	used Usage
	held map[execution.ID]*Reservation
}

// NewMemory builds a store holding used as already settled. Nil starts empty.
func NewMemory(used Usage) *Memory {
	if used == nil {
		used = make(Usage)
	}

	return &Memory{
		used: used,
		held: make(map[execution.ID]*Reservation),
	}
}

// Reserve records r if every charge fits against settled plus reserved usage.
func (m *Memory) Reserve(_ context.Context, r *Reservation) (bool, Usage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	standing := m.standing()

	for _, c := range r.Charges {
		key := Key{Provider: r.Provider, Pool: c.Pool, Period: c.Period}

		if c.Amount > 0 && standing[key]+c.Amount > c.Limit {
			return false, standing, nil
		}
	}

	m.held[r.ID] = r

	return true, nil, nil
}

// Settle removes id's reservation and adds actual in the periods it held.
func (m *Memory) Settle(_ context.Context, id execution.ID, actual map[string]int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.settle(id, actual)

	return nil
}

// ReadUsage returns settled plus reserved usage in the given periods.
func (m *Memory) ReadUsage(_ context.Context, periods []string) (Usage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	usage := make(Usage)

	for key, amount := range m.standing() {
		if slices.Contains(periods, key.Period) {
			usage[key] = amount
		}
	}

	return usage, nil
}

// Reap settles the reservations created before the cutoff that settle accepts.
func (m *Memory) Reap(
	ctx context.Context, before time.Time,
	settle func(context.Context, Held) (map[string]int64, bool),
) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	reaped := 0

	for id, r := range m.held {
		if !r.Created.Before(before) {
			continue
		}

		held := Held{
			ID:       id,
			Provider: r.Provider,
			CPU:      r.CPU,
			Memory:   r.Memory,
			Amounts:  make(map[string]int64, len(r.Charges)),
		}

		for _, c := range r.Charges {
			held.Amounts[c.Pool] = c.Amount
		}

		actual, ok := settle(ctx, held)
		if !ok {
			continue
		}

		m.settle(id, actual)
		reaped++
	}

	return reaped, nil
}

// settle does Settle's work. Callers hold the mutex.
func (m *Memory) settle(id execution.ID, actual map[string]int64) {
	r, ok := m.held[id]
	if !ok {
		return
	}

	delete(m.held, id)

	for _, c := range r.Charges {
		if amount := actual[c.Pool]; amount != 0 {
			m.used[Key{Provider: r.Provider, Pool: c.Pool, Period: c.Period}] += amount
		}
	}
}

// standing sums settled and reserved usage. Callers hold the mutex.
func (m *Memory) standing() Usage {
	usage := make(Usage, len(m.used))

	for key, amount := range m.used {
		usage[key] = amount
	}

	for _, r := range m.held {
		for _, c := range r.Charges {
			usage[Key{Provider: r.Provider, Pool: c.Pool, Period: c.Period}] += c.Amount
		}
	}

	return usage
}
