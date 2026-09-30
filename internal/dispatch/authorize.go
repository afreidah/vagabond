// -------------------------------------------------------------------------------
// Rejected Credentials
//
// Author: Alex Freidah
//
// A credential resolved when the server started can expire while it runs, as
// a session token does. Every call dispatch makes to a provider goes through
// here: one the platform rejects as unauthorized has its credential resolved
// again from the configured source and is made once more. Providers only
// classify the rejection and rebuild their client from new bytes.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// authorized makes call against provider, and once more after the registry
// resolved the provider's credential again, when the first was rejected as
// unauthorized and a fresh credential is in place.
func authorized[T any](ctx context.Context, d *Dispatcher, provider plugin.Provider, call func() (T, error)) (T, error) {
	out, err := call()
	if !errors.Is(err, plugin.ErrUnauthorized) {
		return out, err
	}

	retry, refreshErr := d.registry.Recredential(ctx, provider.Name())
	if refreshErr != nil {
		return out, errors.Join(err, refreshErr)
	}

	if !retry {
		return out, err
	}

	return call()
}

// status asks provider about id, through authorized, since every loop that
// follows an execution starts here.
func (d *Dispatcher) status(ctx context.Context, provider plugin.Provider, id execution.ID) (execution.Status, error) {
	return authorized(ctx, d, provider, func() (execution.Status, error) { return provider.Status(ctx, id) })
}

// authorizedErr is authorized for a call that returns only an error.
func authorizedErr(ctx context.Context, d *Dispatcher, provider plugin.Provider, call func() error) error {
	_, err := authorized(ctx, d, provider, func() (struct{}, error) { return struct{}{}, call() })

	return err
}
