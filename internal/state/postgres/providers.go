// -------------------------------------------------------------------------------
// Disabled Providers
//
// Author: Alex Freidah
//
// Reads and writes the disabled_providers table. Each call is a single
// statement, so none needs the retrying transaction other writes use. If two
// servers disable the same provider at once, both calls succeed and the row
// keeps whichever was written first.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"fmt"

	"github.com/afreidah/vagabond/internal/state"
	db "github.com/afreidah/vagabond/internal/state/postgres/sqlc"
)

// DisableProvider stores that an operator turned the named provider off. If
// it is already off, the stored time and name are kept.
func (s *Store) DisableProvider(ctx context.Context, name string, d state.Disabled) error {
	err := s.queries.DisableProvider(ctx, db.DisableProviderParams{
		Name: name, DisabledAt: d.At, DisabledBy: d.By,
	})
	if err != nil {
		return fmt.Errorf("disable provider %s: %w", name, err)
	}

	return nil
}

// EnableProvider deletes the named provider's row, turning it back on. A
// provider that was not disabled is left alone.
func (s *Store) EnableProvider(ctx context.Context, name string) error {
	if err := s.queries.EnableProvider(ctx, name); err != nil {
		return fmt.Errorf("enable provider %s: %w", name, err)
	}

	return nil
}

// DisabledProviders returns every provider turned off through the API, keyed
// by name. An empty map means none is.
func (s *Store) DisabledProviders(ctx context.Context) (map[string]state.Disabled, error) {
	rows, err := s.queries.ListDisabledProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list disabled providers: %w", err)
	}

	out := make(map[string]state.Disabled, len(rows))
	for _, row := range rows {
		out[row.Name] = state.Disabled{At: row.DisabledAt, By: row.DisabledBy}
	}

	return out, nil
}
