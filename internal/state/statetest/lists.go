// -------------------------------------------------------------------------------
// Listing Contract
//
// Author: Alex Freidah
//
// What an execution store promises about listing records: a namespace's newest
// first up to a limit, narrowed by job, state and provider, and a dispatch's
// oldest first. Also whether a job has a dispatch still running.
// -------------------------------------------------------------------------------

package statetest

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// CONTRACT
// -------------------------------------------------------------------------

// ListContract runs every listing rule against a fresh store from newStore.
func ListContract(t *testing.T, newStore func(t *testing.T) Store) {
	check(t, listRules, newStore)
}

// listRules are the promises ListContract checks.
var listRules = []rule[Store]{
	{"a namespace lists newest first, up to the limit", newestFirst},
	{"job, state and provider each narrow the list", filtersNarrow},
	{"a dispatch lists oldest first", dispatchOldestFirst},
	{"a job has a running dispatch until its verdict", runningUntilVerdict},
}

// -------------------------------------------------------------------------
// RULES
// -------------------------------------------------------------------------

// newestFirst: of three records in default and one in research, default's
// listing is its own newest two, carrying their dispatch and job version.
func newestFirst(t *testing.T, s Store) {
	d := dispatched(t, s, held())

	first := recorded(t, s, d, time.Time{})
	second := recorded(t, s, d, time.Time{})
	third := recorded(t, s, d, time.Time{}, func(r *execution.Record) { r.JobVersion = 3 })

	recorded(t, s, d, time.Time{}, func(r *execution.Record) { r.Namespace = "research" })

	got := listed(t, s, execution.Filter{Namespace: "default", Limit: 2})
	if diff := cmp.Diff(ids(third, second), ids(got...)); diff != "" {
		t.Fatalf("ListExecutions(default, limit 2) (-want +got):\n%s", diff)
	}

	if got[0].Dispatch != d.ID || got[0].Job != d.Job || got[0].JobVersion != 3 {
		t.Errorf("record = %+v, want dispatch %s of %s at version 3", got[0], d.ID, d.Job)
	}

	if all := listed(t, s, execution.Filter{Namespace: "default", Limit: 10}); len(all) != 3 || all[2].ID != first.ID {
		t.Errorf("ListExecutions(default, limit 10) = %v, want all three, oldest last", ids(all...))
	}
}

// filtersNarrow: each of job, state and provider picks only its record, and
// all three together pick only the record matching every one.
func filtersNarrow(t *testing.T, s Store) {
	d := dispatched(t, s, held())

	plain := recorded(t, s, d, time.Time{})
	deploy := recorded(t, s, d, time.Time{}, func(r *execution.Record) { r.Job = "deploy" })
	done := recorded(t, s, d, time.Now().UTC().Truncate(time.Microsecond))
	cloud := recorded(t, s, d, time.Time{}, func(r *execution.Record) { r.Provider = "cloud" })

	cases := []struct {
		filter execution.Filter
		want   *execution.Record
	}{
		{execution.Filter{Job: "deploy"}, deploy},
		{execution.Filter{State: execution.StateSucceeded}, done},
		{execution.Filter{Provider: "cloud"}, cloud},
		{execution.Filter{Job: "ci", State: execution.StateRunning, Provider: "box"}, plain},
	}

	for _, c := range cases {
		c.filter.Namespace, c.filter.Limit = "default", 10

		if diff := cmp.Diff(ids(c.want), ids(listed(t, s, c.filter)...)); diff != "" {
			t.Errorf("ListExecutions(%+v) (-want +got):\n%s", c.filter, diff)
		}
	}
}

// dispatchOldestFirst: a dispatch's executions come back in the order they
// were created, and none of another dispatch's.
func dispatchOldestFirst(t *testing.T, s Store) {
	d := dispatched(t, s, held())

	first := recorded(t, s, d, time.Time{})
	second := recorded(t, s, d, time.Time{})

	recorded(t, s, dispatched(t, s, held()), time.Time{})

	got, err := s.DispatchExecutions(t.Context(), d.ID)
	if err != nil {
		t.Fatalf("DispatchExecutions() = %v", err)
	}

	if diff := cmp.Diff(ids(first, second), ids(got...)); diff != "" {
		t.Errorf("DispatchExecutions() (-want +got):\n%s", diff)
	}
}

// runningUntilVerdict: a job with a dispatch still running has one, another
// job does not, and once the verdict is recorded neither does.
func runningUntilVerdict(t *testing.T, s Store) {
	d := dispatched(t, s, held())

	running := func(job string) bool {
		t.Helper()

		got, err := s.RunningDispatch(t.Context(), d.Namespace, job)
		if err != nil {
			t.Fatalf("RunningDispatch(%s) = %v", job, err)
		}

		return got
	}

	if !running(d.Job) || running("deploy") {
		t.Fatalf("before the verdict: %s running %v, deploy running %v; want true, false",
			d.Job, running(d.Job), running("deploy"))
	}

	verdict := *d
	verdict.State, verdict.Ended = execution.DispatchFailed, time.Now().UTC().Truncate(time.Microsecond)

	if err := s.FinishDispatch(t.Context(), &verdict); err != nil {
		t.Fatalf("FinishDispatch() = %v", err)
	}

	if running(d.Job) {
		t.Errorf("RunningDispatch(%s) after the verdict = true, want false", d.Job)
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// listed lists what f picks, failing the test on an error.
func listed(t *testing.T, s Store, f execution.Filter) []*execution.Record {
	t.Helper()

	got, err := s.ListExecutions(t.Context(), f)
	if err != nil {
		t.Fatalf("ListExecutions() = %v", err)
	}

	return got
}

// ids is the IDs of records, in order, for comparing listings.
func ids(records ...*execution.Record) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.ID.String())
	}

	return out
}
