// -------------------------------------------------------------------------------
// Reaping Abandoned Reservations
//
// Author: Alex Freidah
//
// A process that dies between reserving and settling leaves its reservation
// holding quota. The ledger finds the stale ones; this asks each provider what
// became of the execution, because only dispatch knows the providers.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"time"

	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/plugin"
)

// Reap resolves stale quota reservations and reports how many were settled.
func (d *Dispatcher) Reap(ctx context.Context) (int, error) {
	return d.ledger.Reap(ctx, d.resolve)
}

// resolve asks the execution's provider what it did.
//
// Anything short of an answer keeps the reservation, which is the over-count.
// A provider with no status, whose work finished inside Submit, did run, for a
// time nobody recorded, so its estimate stands as the charge.
func (d *Dispatcher) resolve(ctx context.Context, h ledger.Held) (ledger.Verdict, time.Duration) {
	provider, ok := d.registry.Provider(h.Provider)
	if !ok {
		return ledger.Keep, 0
	}

	status, err := provider.Status(ctx, h.ID)

	switch {
	case errors.Is(err, plugin.ErrUnknownExecution):
		return ledger.Drop, 0
	case errors.Is(err, plugin.ErrUnsupported):
		return ledger.Stand, 0
	case err != nil, !status.State.Terminal():
		return ledger.Keep, 0
	case status.StartedAt.IsZero():
		return ledger.Finished, 0
	case status.EndedAt.IsZero():
		return ledger.Stand, 0
	default:
		return ledger.Finished, status.EndedAt.Sub(status.StartedAt)
	}
}
