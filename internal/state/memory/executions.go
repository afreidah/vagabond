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
	"slices"
	"strings"
	"sync"

	"github.com/afreidah/vagabond/internal/execution"
)

// Executions holds dispatches and execution records by ID.
type Executions struct {
	mu         sync.Mutex
	dispatches map[execution.ID]execution.Dispatch
	records    map[execution.ID]execution.Record
}

// NewExecutions returns an empty store.
func NewExecutions() *Executions {
	return &Executions{
		dispatches: make(map[execution.ID]execution.Dispatch),
		records:    make(map[execution.ID]execution.Record),
	}
}

// CreateDispatch records a run as it starts. An ID already recorded is
// refused.
func (s *Executions) CreateDispatch(_ context.Context, d *execution.Dispatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.dispatches[d.ID]; ok {
		return fmt.Errorf("dispatch %s already recorded", d.ID)
	}

	s.dispatches[d.ID] = *d

	return nil
}

// FinishDispatch records how a run ended, only while it is still running, and
// keeps the time it was created.
func (s *Executions) FinishDispatch(_ context.Context, d *execution.Dispatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.dispatches[d.ID]
	if !ok || stored.State != execution.DispatchRunning {
		return fmt.Errorf("%w: dispatch %s", execution.ErrStale, d.ID)
	}

	stored.State, stored.Error, stored.Ended = d.State, d.Error, d.Ended
	s.dispatches[d.ID] = stored

	return nil
}

// GetDispatch returns a copy of the dispatch record for id.
func (s *Executions) GetDispatch(_ context.Context, id execution.ID) (*execution.Dispatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.dispatches[id]
	if !ok {
		return nil, fmt.Errorf("%w: dispatch %s", execution.ErrNotFound, id)
	}

	return &stored, nil
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

// DispatchExecutions returns copies of every record of one dispatch, oldest
// first.
func (s *Executions) DispatchExecutions(_ context.Context, dispatch execution.ID) ([]*execution.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*execution.Record

	for id := range s.records {
		if stored := s.records[id]; stored.Dispatch == dispatch {
			r := clone(&stored)
			out = append(out, &r)
		}
	}

	slices.SortFunc(out, func(a, b *execution.Record) int {
		return strings.Compare(a.ID.String(), b.ID.String())
	})

	return out, nil
}

// clone copies r so neither the caller nor the store can change the other's.
func clone(r *execution.Record) execution.Record {
	out := *r
	out.Result = r.Result.Bounded()

	return out
}
