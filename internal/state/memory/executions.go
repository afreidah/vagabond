// -------------------------------------------------------------------------------
// Memory Execution Store
//
// Author: Alex Freidah
//
// Execution records in a map, for local mode without a database and for tests.
// The same rules as the Postgres store: an update names the state it was read
// in and is refused if the record has moved on. Lost with the process.
// -------------------------------------------------------------------------------

package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/afreidah/vagabond/internal/execution"
)

// Executions holds records by ID.
type Executions struct {
	mu      sync.Mutex
	records map[execution.ID]execution.Record
}

// NewExecutions returns an empty store.
func NewExecutions() *Executions {
	return &Executions{records: make(map[execution.ID]execution.Record)}
}

// Create records a new execution. An ID already recorded is refused, since the
// ID is the submission idempotency key.
func (s *Executions) Create(_ context.Context, r *execution.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.records[r.ID]; ok {
		return fmt.Errorf("execution %s already recorded", r.ID)
	}

	s.records[r.ID] = clone(r)

	return nil
}

// Update writes r over the stored record if it is still in from.
func (s *Executions) Update(_ context.Context, r *execution.Record, from execution.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.records[r.ID]
	if !ok || stored.State != from {
		return fmt.Errorf("%w: %s", execution.ErrStale, r.ID)
	}

	s.records[r.ID] = clone(r)

	return nil
}

// Get returns a copy of the record for id.
func (s *Executions) Get(_ context.Context, id execution.ID) (*execution.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.records[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", execution.ErrNotFound, id)
	}

	out := clone(&stored)

	return &out, nil
}

// clone copies r so neither the caller nor the store can change the other's.
func clone(r *execution.Record) execution.Record {
	out := *r
	out.Result = r.Result.Bounded()

	return out
}
