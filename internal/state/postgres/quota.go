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

// -------------------------------------------------------------------------
// LEDGER
// -------------------------------------------------------------------------

// Compile-time proof the ledger's store contract is met.
var _ ledger.Store = (*Store)(nil)

// Reserve records r only if every charge fits. When it does not, the usage
// the refusal was decided against is read in the same transaction, so the
// ledger names a pool that really was full.
func (s *Store) Reserve(ctx context.Context, r *ledger.Reservation) (bool, ledger.Usage, error) {
	params := db.ReserveQuotaParams{
		ExecutionID: r.ID.String(),
		Provider:    r.Provider,
		CreatedAt:   r.Created,
	}

	for _, c := range r.Charges {
		params.Namespaces = append(params.Namespaces, c.Namespace)
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
	if err != nil {
		return false, nil, err
	}

	return fits, standing, nil
}

// Settle replaces id's reservation with actual, by pool.
func (s *Store) Settle(ctx context.Context, id execution.ID, actual map[ledger.PoolRef]int64) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		return settleOne(ctx, q, id, actual)
	})
}

// Reserved reports whether id still holds a reservation.
func (s *Store) Reserved(ctx context.Context, id execution.ID) (bool, error) {
	held, err := s.queries.ExecutionReserved(ctx, id.String())
	if err != nil {
		return false, fmt.Errorf("reading the reservation of %s: %w", id, err)
	}

	return held, nil
}

// ReadUsage returns settled plus reserved usage in the given periods, from one
// snapshot, so a settle moving an amount between them is seen whole or not.
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

// Reap claims reservations created before the cutoff and drops those orphaned
// accepts, charging nothing.
//
// The claim holds its row locks while orphaned reads the execution record,
// keeping two reapers off one execution. A retry asks again; the questions
// are reads.
func (s *Store) Reap(ctx context.Context, before time.Time, orphaned ledger.Orphaned) (int, error) {
	var reaped int

	err := s.serializable(ctx, func(q *db.Queries) error {
		reaped = 0

		rows, err := q.ClaimStaleReservations(ctx, before)
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}

		ids, err := distinctIDs(rows)
		if err != nil {
			return err
		}

		for _, id := range ids {
			if !orphaned(ctx, id) {
				continue
			}

			if err := settleOne(ctx, q, id, nil); err != nil {
				return err
			}

			reaped++
		}

		return nil
	})

	return reaped, err
}

// distinctIDs parses claimed reservation rows into execution IDs, once each.
// Rows arrive ordered by execution, one per pool, so repeats are adjacent.
func distinctIDs(rows []string) ([]execution.ID, error) {
	var ids []execution.ID

	for i, row := range rows {
		if i > 0 && rows[i-1] == row {
			continue
		}

		id, err := execution.ParseID(row)
		if err != nil {
			return nil, fmt.Errorf("reservation row: %w", err)
		}

		ids = append(ids, id)
	}

	return ids, nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// settleOne runs SettleQuota with actual flattened into its arrays.
func settleOne(ctx context.Context, q *db.Queries, id execution.ID, actual map[ledger.PoolRef]int64) error {
	params := db.SettleQuotaParams{ExecutionID: id.String()}

	for ref, amount := range actual {
		params.Namespaces = append(params.Namespaces, ref.Namespace)
		params.Pools = append(params.Pools, ref.Pool)
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
		usage[ledger.Key{
			Namespace: row.Namespace, Provider: row.Provider, Pool: row.Pool, Period: row.Period,
		}] = row.Used
	}

	return usage
}
