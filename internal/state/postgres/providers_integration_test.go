//go:build integration

// -------------------------------------------------------------------------------
// Disabled Provider Store Integration Tests
//
// Author: Alex Freidah
//
// The disabled provider rules, run against Postgres and CockroachDB with the
// engines and open helper from the store suite.
// -------------------------------------------------------------------------------

package postgres_test

import (
	"testing"

	"github.com/afreidah/vagabond/internal/state/statetest"
)

// Both engines keep the disabled provider rules every store keeps.
func TestProviders_Contract(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			statetest.ProviderContract(t, func(t *testing.T) statetest.ProviderStore {
				return open(t.Context(), t, e)
			})
		})
	}
}
