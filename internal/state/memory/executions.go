// -------------------------------------------------------------------------------
// Memory Execution Store
//
// Author: Alex Freidah
//
// Dispatches, execution records and their output in maps, for local mode
// without a database and for tests. The same rules as the Postgres store:
// executions and output are written only under the dispatch's lease. Lost
// with the process.
// -------------------------------------------------------------------------------

package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Executions holds dispatches, execution records and their output by ID, the
// output as chunks by number.
type Executions struct {
	mu         sync.Mutex
	dispatches map[execution.ID]execution.Dispatch
	records    map[execution.ID]execution.Record
	logs       map[execution.ID]map[int64][]byte
}

// NewExecutions returns an empty store.
func NewExecutions() *Executions {
	return &Executions{
		dispatches: make(map[execution.ID]execution.Dispatch),
		records:    make(map[execution.ID]execution.Record),
		logs:       make(map[execution.ID]map[int64][]byte),
	}
}

// -------------------------------------------------------------------------
// DISPATCHES
// -------------------------------------------------------------------------

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

// FinishDispatch records the verdict of a run still running under d's lease,
// and keeps the time it was created.
func (s *Executions) FinishDispatch(_ context.Context, d *execution.Dispatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.held(d.ID, d.Lease())
	if !ok || stored.State != execution.DispatchRunning {
		return fmt.Errorf("%w: dispatch %s", execution.ErrStale, d.ID)
	}

	stored.State, stored.Error, stored.Ended = d.State, d.Error, d.Ended
	s.dispatches[d.ID] = stored

	return nil
}

// CloseDispatch records under lease that every execution of the dispatch
// finished through release, which ends the lease.
func (s *Executions) CloseDispatch(_ context.Context, id execution.ID, lease execution.Lease, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.held(id, lease)
	if !ok {
		return fmt.Errorf("%w: dispatch %s", execution.ErrStale, id)
	}

	stored.Done = at
	s.dispatches[id] = stored

	return nil
}

// RenewDispatch extends lease on an open dispatch to until and returns when a
// cancel was requested, zero if none. Stale once done or claimed.
func (s *Executions) RenewDispatch(
	_ context.Context, id execution.ID, lease execution.Lease, until time.Time,
) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.held(id, lease)
	if !ok {
		return time.Time{}, fmt.Errorf("%w: dispatch %s", execution.ErrStale, id)
	}

	stored.LeaseUntil = until
	s.dispatches[id] = stored

	return stored.CancelRequested, nil
}

// RequestCancel asks whoever holds an open dispatch to cancel it. The first
// request's time stands.
func (s *Executions) RequestCancel(_ context.Context, id execution.ID, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.dispatches[id]
	if ok && stored.Done.IsZero() && stored.CancelRequested.IsZero() {
		stored.CancelRequested = at
		s.dispatches[id] = stored
	}

	return nil
}

// ClaimDispatches takes every open dispatch whose lease lapsed before now,
// except those in skip, leasing each to owner until until under its next epoch.
func (s *Executions) ClaimDispatches(
	_ context.Context, owner string, now, until time.Time, skip []execution.ID,
) ([]*execution.Dispatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var claimed []*execution.Dispatch

	for id := range s.dispatches {
		stored := s.dispatches[id]
		if !stored.Done.IsZero() || !stored.LeaseUntil.Before(now) || slices.Contains(skip, id) {
			continue
		}

		stored.Owner, stored.LeaseUntil = owner, until
		stored.Epoch++
		s.dispatches[id] = stored
		claimed = append(claimed, &stored)
	}

	return claimed, nil
}

// held returns the dispatch for id when it is open and held under lease.
// Callers hold the mutex.
func (s *Executions) held(id execution.ID, lease execution.Lease) (execution.Dispatch, bool) {
	stored, ok := s.dispatches[id]

	return stored, ok && stored.Done.IsZero() && stored.Lease() == lease
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

// Ping reports the store reachable, which memory always is.
func (s *Executions) Ping(context.Context) error {
	return nil
}

// -------------------------------------------------------------------------
// RECORDS
// -------------------------------------------------------------------------

// Create records a new execution while its dispatch is held under lease.
// Recording an ID twice fails; a lease no longer held is execution.ErrStale.
func (s *Executions) Create(_ context.Context, r *execution.Record, lease execution.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.records[r.ID]; ok {
		return fmt.Errorf("execution %s already recorded", r.ID)
	}

	if _, ok := s.held(r.Dispatch, lease); !ok {
		return stale(r.ID)
	}

	s.records[r.ID] = clone(r)

	return nil
}

// Save writes r over the stored record, whatever its state, while r's
// dispatch is held under lease; otherwise execution.ErrStale.
func (s *Executions) Save(_ context.Context, r *execution.Record, lease execution.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.holds(r.ID, lease) {
		return stale(r.ID)
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

// jobExecutions returns copies of a job's most recent records, newest first,
// at most limit of them.
func (s *Executions) jobExecutions(namespace, job string, limit int) []*execution.Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*execution.Record

	for id := range s.records {
		if stored := s.records[id]; stored.Namespace == namespace && stored.Job == job {
			r := clone(&stored)
			out = append(out, &r)
		}
	}

	slices.SortFunc(out, func(a, b *execution.Record) int {
		return strings.Compare(b.ID.String(), a.ID.String())
	})

	return out[:min(limit, len(out))]
}

// holds reports whether the dispatch of execution id is open and held under
// lease. Callers hold the mutex.
func (s *Executions) holds(id execution.ID, lease execution.Lease) bool {
	stored, ok := s.records[id]
	if !ok {
		return false
	}

	_, held := s.held(stored.Dispatch, lease)

	return held
}

// -------------------------------------------------------------------------
// OUTPUT
// -------------------------------------------------------------------------

// AppendLog stores chunk seq of id's output under lease, dropping every chunk
// before keepFrom and every one from seq on, so a write at 0 starts over.
func (s *Executions) AppendLog(
	_ context.Context, id execution.ID, seq int64, data []byte, keepFrom int64, lease execution.Lease,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.holds(id, lease) {
		return stale(id)
	}

	chunks, ok := s.logs[id]
	if !ok {
		chunks = make(map[int64][]byte)
		s.logs[id] = chunks
	}

	maps.DeleteFunc(chunks, func(k int64, _ []byte) bool { return k < keepFrom || k >= seq })
	chunks[seq] = bytes.Clone(data)

	return nil
}

// ReadLog writes id's stored output from chunk fromSeq on to w, in order. An
// execution with none writes nothing.
func (s *Executions) ReadLog(_ context.Context, id execution.ID, fromSeq int64, w io.Writer) error {
	// A chunk is never changed once stored, so it is written outside the lock.
	s.mu.Lock()
	chunks := maps.Clone(s.logs[id])
	s.mu.Unlock()

	for _, seq := range slices.Sorted(maps.Keys(chunks)) {
		if seq < fromSeq {
			continue
		}

		if _, err := w.Write(chunks[seq]); err != nil {
			return err
		}
	}

	return nil
}

// PruneLogs deletes the output of every execution that ended before before,
// returning how many lost theirs.
func (s *Executions) PruneLogs(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pruned := 0

	for id := range s.logs {
		if ended := s.records[id].EndedAt; !ended.IsZero() && ended.Before(before) {
			delete(s.logs, id)

			pruned++
		}
	}

	return pruned, nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// stale reports a write to id refused because its dispatch is no longer held
// under the caller's lease.
func stale(id execution.ID) error {
	return fmt.Errorf("%w: %s is no longer held under this lease", execution.ErrStale, id)
}

// clone copies r so neither the caller nor the store can change the other's.
// A result's own copy of the output is not kept: output is stored apart.
func clone(r *execution.Record) execution.Record {
	out := *r

	if r.Result != nil {
		result := *r.Result
		result.Logs, result.LogsTruncated = nil, false
		out.Result = &result
	}

	return out
}
