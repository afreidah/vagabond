// -------------------------------------------------------------------------------
// Release Loop Tests
//
// Author: Alex Freidah
//
// What providers leave behind is released once the execution is over and its
// charge settled, whichever way it ended; nothing is released while it may
// still be running, while dispatch may still be reading it, or before the
// reaper has asked the provider what it cost.
// -------------------------------------------------------------------------------

package dispatch

import (
	"errors"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// releasing registers a releasing provider named "a" that ends in state after
// polls Status calls.
func releasing(state execution.State, polls int) (*releasingProvider, *fakeRegistry) {
	p := &releasingProvider{scriptedProvider: scriptedProvider{
		name: "a", pollsToFinish: polls, finalState: state,
	}}

	reg := newRegistry(&p.scriptedProvider)
	reg.providers["a"] = p

	return p, reg
}

// leftover records an execution on provider as dispatch leaves one it stopped
// following: no result, last changed age ago.
func leftover(t *testing.T, reg *fakeRegistry, provider string, age time.Duration) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	rec := &execution.Record{Namespace: ns, Provider: provider}
	rec.ID, rec.State, rec.UpdatedAt = id, execution.StateCancelled, time.Now().Add(-age)

	if err := reg.executions.Create(t.Context(), rec); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	return id
}

// releaseLoop runs one pass of the loop and returns how many it released.
func releaseLoop(t *testing.T, reg *fakeRegistry) (int, error) {
	t.Helper()

	return newDispatcher(t, reg).ReleaseLeftovers(t.Context())
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// A run dispatch stopped following, once the provider says it is over, is
// released and marked.
func TestReleaseLoop_ReleasesAnEndedRun(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateCancelled, 1)
	id := leftover(t, reg, "a", 2*ReleaseAfter)

	released, err := releaseLoop(t, reg)
	if err != nil || released != 1 || p.releases != 1 {
		t.Fatalf("released %d (%d calls), %v; want 1", released, p.releases, err)
	}

	if record(t, reg, id).Released.IsZero() {
		t.Error("the record was not marked released")
	}
}

// An execution the provider still reports running is left for a later pass.
func TestReleaseLoop_LeavesARunningExecution(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateSucceeded, 100)
	id := leftover(t, reg, "a", 2*ReleaseAfter)

	if released, err := releaseLoop(t, reg); err != nil || released != 0 || p.releases != 0 {
		t.Errorf("released %d (%d calls), %v; want none", released, p.releases, err)
	}

	if !record(t, reg, id).Released.IsZero() {
		t.Error("a running execution was marked released")
	}
}

// An execution that changed recently may still be read by dispatch, so it is
// not even asked about.
func TestReleaseLoop_LeavesARecentExecutionAlone(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateCancelled, 1)
	leftover(t, reg, "a", time.Minute)

	if released, err := releaseLoop(t, reg); err != nil || released != 0 || p.polls != 0 {
		t.Errorf("released %d after %d status calls, %v; want it untouched", released, p.polls, err)
	}
}

// A reservation is settled by asking the provider what ran, so the execution
// is not released until the reaper has done that.
func TestReleaseLoop_WaitsForTheReservationToSettle(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateCancelled, 1)
	id := leftover(t, reg, "a", 2*ReleaseAfter)

	if err := reg.ledger.Reserve(t.Context(), id, ns, "a", quota.Execution{CPU: 1000, Memory: 512}); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}

	if released, _ := releaseLoop(t, reg); released != 0 || p.releases != 0 {
		t.Fatalf("released %d with the reservation standing, want 0", released)
	}

	if err := reg.ledger.Settle(t.Context(), id, ns, "a", quota.Execution{}); err != nil {
		t.Fatalf("Settle() = %v", err)
	}

	if released, err := releaseLoop(t, reg); err != nil || released != 1 {
		t.Errorf("released %d after settling, %v; want 1", released, err)
	}
}

// A release that fails leaves the record unreleased, and the next pass tries
// again.
func TestReleaseLoop_RetriesAFailedRelease(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateCancelled, 1)
	id := leftover(t, reg, "a", 2*ReleaseAfter)
	p.releaseErr = errors.New("node offline")

	if released, err := releaseLoop(t, reg); err == nil || released != 0 {
		t.Fatalf("released %d, %v; want the failure reported", released, err)
	}

	if !record(t, reg, id).Released.IsZero() {
		t.Fatal("a failed release was marked released")
	}

	p.releaseErr = nil

	if released, err := releaseLoop(t, reg); err != nil || released != 1 {
		t.Errorf("released %d on the retry, %v; want 1", released, err)
	}
}

// An execution the provider has no record of has nothing left to release.
func TestReleaseLoop_MarksAnUnknownExecution(t *testing.T) {
	t.Parallel()

	p, reg := releasing(execution.StateCancelled, 1)
	p.statusErr = plugin.ErrUnknownExecution
	id := leftover(t, reg, "a", 2*ReleaseAfter)

	if released, err := releaseLoop(t, reg); err != nil || released != 1 || p.releases != 0 {
		t.Errorf("released %d (%d calls), %v; want it marked without a call", released, p.releases, err)
	}

	if record(t, reg, id).Released.IsZero() {
		t.Error("the record was not marked released")
	}
}

// A provider that leaves nothing behind is marked without being asked.
func TestReleaseLoop_MarksAProviderWithNothingToRelease(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateCancelled}
	reg := newRegistry(p)
	id := leftover(t, reg, "a", 2*ReleaseAfter)

	if released, err := releaseLoop(t, reg); err != nil || released != 1 || p.polls != 0 {
		t.Errorf("released %d after %d status calls, %v; want it marked unasked", released, p.polls, err)
	}

	if record(t, reg, id).Released.IsZero() {
		t.Error("the record was not marked released")
	}
}

// Dispatch releasing after the result marks the record, so the loop leaves it.
func TestReleaseAfterTheResultMarksTheRecord(t *testing.T) {
	t.Parallel()

	_, reg := releasing(execution.StateSucceeded, 1)

	outcome, err := newDispatcher(t, reg).RunTask(t.Context(), origin, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask() = %v", err)
	}

	if record(t, reg, outcome.ID).Released.IsZero() {
		t.Error("the record was not marked released")
	}
}
