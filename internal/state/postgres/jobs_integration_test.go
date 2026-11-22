//go:build integration

// -------------------------------------------------------------------------------
// Registered Job Store Integration Tests
//
// Author: Alex Freidah
//
// The registered job rules, periodic launches included, run against Postgres
// and CockroachDB with the engines and open helper from the store suite.
// -------------------------------------------------------------------------------

package postgres_test

import (
	"testing"

	"github.com/afreidah/vagabond/internal/state/statetest"
)

// Both engines keep the registered job rules every store keeps.
func TestJobs_Contract(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			statetest.JobContract(t, func(t *testing.T) statetest.JobStore {
				return open(t.Context(), t, e)
			})
		})
	}
}
