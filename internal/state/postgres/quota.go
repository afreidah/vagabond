// -------------------------------------------------------------------------------
// Quota Persistence
//
// Author: Alex Freidah
//
// The ledger's store. Every call runs in a serializable transaction, retried on
// a serialization failure, so a reservation's headroom test and its insert
// cannot interleave with another process's.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/ledger"
	db "github.com/afreidah/vagabond/internal/state/postgres/sqlc"
)

var _ ledger.Store = (*Store)(nil)

// Reserve records r only if every charge fits. When it does not, the usage
// the refusal was decided against is read in the same transaction, so the
// ledger names a pool that really was full.
func (s *Store) Reserve(ctx context.Context, r ledger.Reservation) (bool, ledger.Usage, error) {
	params := db.ReserveQuotaParams{
		ExecutionID: r.ID.String(),
		Provider:    r.Provider,
		Cpu:         int64(r.CPU),
		Memory:      int64(r.Memory),
		CreatedAt:   r.Created,
	}

	for _, c := range r.Charges {
		params.Pools = append(params.Pools, c.Pool)
		params.Periods = append(params.Periods, c.Period)
		params.Amounts = append(params.Amounts, c.Amount)
		params.Limits = append(params.Limits, c.Limit)
	}

	var (
		fits     bool
		standing ledger.Usage
	)

	err := s.serializable(ctx, func(q *db.Queries) error {
		inserted, err := q.ReserveQuota(ctx, params)
		if err != nil {
			return fmt.Errorf("reserve: %w", err)
		}

		fits = inserted > 0
		if fits {
			return nil
		}

		rows, err := q.ReadQuotaUsage(ctx, params.Periods)
		if err != nil {
			return fmt.Errorf("read usage: %w", err)
		}

		standing = usageFromRows(rows)

		return nil
	})

	return fits, standing, err
}

// Settle replaces id's reservation with actual, by pool.
func (s *Store) Settle(ctx context.Context, id execution.ID, actual map[string]int64) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		return settleOne(ctx, q, id, actual)
	})
}

// ReadUsage returns settled plus reserved usage in the given periods.
//
// One statement reads both tables from one snapshot, so a settle moving an
// amount between them is seen whole or not at all.
func (s *Store) ReadUsage(ctx context.Context, periods []string) (ledger.Usage, error) {
	if len(periods) == 0 {
		return ledger.Usage{}, nil
	}

	rows, err := s.queries.ReadQuotaUsage(ctx, periods)
	if err != nil {
		return nil, fmt.Errorf("read usage: %w", err)
	}

	return usageFromRows(rows), nil
}

// Reap claims reservations created before the cutoff and settles those that
// settle returns true for.
//
// The claim holds its row locks while settle asks providers, which is what
// keeps two reapers off the same execution. A retry after a serialization
// failure asks again; the questions are reads, so asking twice is harmless.
func (s *Store) Reap(
	ctx context.Context, before time.Time,
	settle func(context.Context, ledger.Held) (map[string]int64, bool),
) (int, error) {
	var reaped int

	err := s.serializable(ctx, func(q *db.Queries) error {
		reaped = 0

		rows, err := q.ClaimStaleReservations(ctx, before)
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}

		held, err := heldFromRows(rows)
		if err != nil {
			return err
		}

		for _, h := range held {
			actual, ok := settle(ctx, h)
			if !ok {
				continue
			}

			if err := settleOne(ctx, q, h.ID, actual); err != nil {
				return err
			}

			reaped++
		}

		return nil
	})

	return reaped, err
}

// settleOne runs SettleQuota with actual flattened into its arrays.
func settleOne(ctx context.Context, q *db.Queries, id execution.ID, actual map[string]int64) error {
	params := db.SettleQuotaParams{ExecutionID: id.String()}

	for pool, amount := range actual {
		params.Pools = append(params.Pools, pool)
		params.Amounts = append(params.Amounts, amount)
	}

	if err := q.SettleQuota(ctx, params); err != nil {
		return fmt.Errorf("settle %s: %w", id, err)
	}

	return nil
}

// usageFromRows keys counters the way the ledger reads them.
func usageFromRows(rows []db.ReadQuotaUsageRow) ledger.Usage {
	usage := make(ledger.Usage, len(rows))

	for _, row := range rows {
		usage[ledger.Key{Provider: row.Provider, Pool: row.Pool, Period: row.Period}] = row.Used
	}

	return usage
}

// heldFromRows groups claimed rows by execution. Rows arrive ordered by
// execution, so each group is contiguous.
func heldFromRows(rows []db.QuotaReservation) ([]ledger.Held, error) {
	var held []ledger.Held

	for _, row := range rows {
		id, err := execution.ParseID(row.ExecutionID)
		if err != nil {
			return nil, fmt.Errorf("reservation row: %w", err)
		}

		if len(held) == 0 || held[len(held)-1].ID != id {
			held = append(held, ledger.Held{
				ID:       id,
				Provider: row.Provider,
				CPU:      int(row.Cpu),
				Memory:   int(row.Memory),
				Amounts:  make(map[string]int64),
			})
		}

		held[len(held)-1].Amounts[row.Pool] = row.Amount
	}

	return held, nil
}
