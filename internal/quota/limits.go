// -------------------------------------------------------------------------------
// Compiled Quota Pools
//
// Author: Alex Freidah
//
// Turns the budgets an operator writes in config into the form the ledger
// reads. Pools are additive: an execution charges every pool whose meter it
// touches and is admitted only when all of them have headroom, which is what
// lets a daily cap sit inside a monthly one.
// -------------------------------------------------------------------------------

package quota

import (
	"errors"
	"fmt"
	"math"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// PoolSpec is one budget as written in config, before compilation. Limit is in
// the meter's natural unit.
type PoolSpec struct {
	Name   string
	Meter  Meter
	Limit  int64
	Period Period
}

// Pool is a compiled budget. Limit is in the meter's base unit so it compares
// directly against a counter.
type Pool struct {
	Name   string
	Meter  Meter
	Period Period
	Limit  int64
}

// Limits is one provider's compiled budgets. The zero value enforces nothing,
// which is what a provider an operator declared no quotas for means.
type Limits struct {
	pools []Pool
}

// PoolUsage is what has been charged to each of one provider's pools, each in
// its own current period. Pools are additive, so these do not sum to a
// provider's total and must not be reported as if they did.
type PoolUsage map[string]int64

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// NewLimits compiles an operator's budgets, refusing a config it cannot
// enforce rather than resolving it silently in one direction.
func NewLimits(specs []PoolSpec) (Limits, error) {
	if len(specs) == 0 {
		return Limits{}, nil
	}

	pools := make([]Pool, 0, len(specs))
	seen := make(map[string]bool, len(specs))

	for _, spec := range specs {
		pool, err := compile(spec, seen)
		if err != nil {
			return Limits{}, err
		}

		seen[pool.Name] = true
		pools = append(pools, pool)
	}

	return Limits{pools: pools}, nil
}

// compile validates one spec and converts its limit to base units.
func compile(spec PoolSpec, seen map[string]bool) (Pool, error) {
	switch {
	case spec.Name == "":
		return Pool{}, errors.New("quota pool has no name")
	case seen[spec.Name]:
		return Pool{}, fmt.Errorf("quota pool %q declared twice", spec.Name)
	case !spec.Meter.Valid():
		return Pool{}, fmt.Errorf("quota pool %q meters unknown %q", spec.Name, spec.Meter)
	case !spec.Period.Valid():
		return Pool{}, fmt.Errorf("quota pool %q resets on unknown period %q", spec.Name, spec.Period)
	case spec.Limit <= 0:
		return Pool{}, fmt.Errorf("quota pool %q has no positive limit", spec.Name)
	}

	limit, err := baseLimit(spec)
	if err != nil {
		return Pool{}, err
	}

	return Pool{
		Name:   spec.Name,
		Meter:  spec.Meter,
		Period: spec.Period,
		Limit:  limit,
	}, nil
}

// baseLimit converts a limit to base units, refusing one large enough that the
// counter comparing against it would overflow.
func baseLimit(spec PoolSpec) (int64, error) {
	scale := spec.Meter.scale()
	if spec.Limit > math.MaxInt64/scale {
		return 0, fmt.Errorf(
			"quota pool %q limit of %d %s is too large to count",
			spec.Name, spec.Limit, spec.Meter.Unit(),
		)
	}

	return spec.Limit * scale, nil
}

// -------------------------------------------------------------------------
// ACCESSORS
// -------------------------------------------------------------------------

// Pools returns every compiled pool in configured order. The ledger reports a
// pool whether or not anything charged it this period.
func (l Limits) Pools() []Pool {
	return l.pools
}

// Unlimited reports whether no pool can refuse an execution, which keeps an
// unconstrained provider off the counter read path entirely. Every compiled
// pool enforces a positive limit, so this is simply whether any were declared.
func (l Limits) Unlimited() bool {
	return len(l.pools) == 0
}

// -------------------------------------------------------------------------
// CHARGING
// -------------------------------------------------------------------------

// Deltas reports what e charges each pool, in base units, omitting the pools
// it does not touch.
func (l Limits) Deltas(e Execution) map[string]int64 {
	if len(l.pools) == 0 {
		return nil
	}

	deltas := make(map[string]int64, len(l.pools))

	for _, p := range l.pools {
		if charge := p.Meter.charge(e); charge > 0 {
			deltas[p.Name] = charge
		}
	}

	return deltas
}

// Within reports whether e fits every pool that could refuse it, given what
// those pools have already been charged this period.
func (l Limits) Within(usage PoolUsage, e Execution) bool {
	for _, p := range l.pools {
		charge := p.Meter.charge(e)
		if charge == 0 {
			continue
		}

		if usage[p.Name]+charge > p.Limit {
			return false
		}
	}

	return true
}
