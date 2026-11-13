// -------------------------------------------------------------------------------
// Reaping Reservations
//
// Author: Alex Freidah
//
// A reservation is made before its execution is recorded, so a process that
// dies between the two, or a record the store refused, leaves a reservation no
// runner will settle. Every recorded execution is settled by its runner.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"

	"github.com/afreidah/vagabond/internal/execution"
)

// Reap drops stale reservations whose execution was never recorded, and
// reports how many it dropped.
func (d *Dispatcher) Reap(ctx context.Context) (int, error) {
	return d.ledger.Reap(ctx, d.orphaned)
}

// orphaned reports whether id has no execution record, so nothing was
// submitted on its reservation. A record that cannot be read keeps the
// reservation for the next pass.
func (d *Dispatcher) orphaned(ctx context.Context, id execution.ID) bool {
	_, err := d.executions.Get(ctx, id)

	return errors.Is(err, execution.ErrNotFound)
}
