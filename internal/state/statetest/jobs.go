// -------------------------------------------------------------------------------
// Registered Job Contract
//
// Author: Alex Freidah
//
// The rules every store follows for registered jobs: a new version only when
// the formatted source changed, a stopped job revived by registering it again,
// and namespaces kept apart. For a periodic job, when its last launch is kept
// or reset, and that a launch is claimed once and never against a stale read.
// -------------------------------------------------------------------------------

package statetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
)

// -------------------------------------------------------------------------
// CONTRACT
// -------------------------------------------------------------------------

// JobStore is the part of a store that keeps registered jobs.
type JobStore interface {
	Register(ctx context.Context, namespace, name string, source []byte, periodic *job.Periodic, now time.Time) (int64, bool, error)
	Stop(ctx context.Context, namespace, name string, now time.Time) error
	Job(ctx context.Context, namespace, name string) (*jobs.Job, error)
	Jobs(ctx context.Context, namespace string) ([]*jobs.Job, error)
	Version(ctx context.Context, namespace, name string, version int64) (*jobs.Version, error)
	Versions(ctx context.Context, namespace, name string) ([]*jobs.Version, error)
	PeriodicJobs(ctx context.Context) ([]*jobs.Job, error)
	ClaimLaunch(ctx context.Context, j *jobs.Job, now time.Time) (bool, error)
}

// JobContract runs every registered job rule against a fresh store from
// newStore.
func JobContract(t *testing.T, newStore func(t *testing.T) JobStore) {
	check(t, jobRules, newStore)
}

// jobRules are the promises JobContract checks.
var jobRules = []rule[JobStore]{
	{"a version is made only on a change, and formatting is not one", versionsOnChange},
	{"a stopped job is kept, and registering it again revives it", stopThenRevive},
	{"namespaces keep same-named jobs apart, and missing jobs are not found", namespacesAndMissing},
	{"only an active schedule is listed, from when it was registered", periodicListed},
	{"a changed version of a periodic job keeps its last launch", launchKept},
	{"a job that becomes periodic again starts from its registration", launchReset},
	{"a launch is claimed once, and never against a stale read", claimedOnce},
}

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// One job's source as first written, reformatted only, and really changed.
const (
	ciSource = `job "ci" {
  type = "batch"
}
`
	ciReformatted = "job   \"ci\"   {\n      type=\"batch\"\n}\n"
	ciChanged     = `job "ci" {
  type = "batch"

  meta {
    team = "platform"
  }
}
`
)

// daily is a periodic block as a job file writes it, and dailyKept the
// schedule a store keeps for it.
var (
	daily     = &job.Periodic{Crons: []string{"@daily"}}
	dailyKept = jobs.Schedule(daily)
)

// -------------------------------------------------------------------------
// RULES
// -------------------------------------------------------------------------

// versionsOnChange: the same source and a reformatted one keep version 1, a
// changed one makes version 2, and the versions read back as registered.
func versionsOnChange(t *testing.T, s JobStore) {
	steps := []struct {
		source  string
		version int64
		changed bool
	}{
		{ciSource, 1, true},
		{ciSource, 1, false},
		{ciReformatted, 1, false},
		{ciChanged, 2, true},
	}

	for i, step := range steps {
		version, changed := registerCI(t, s, step.source, nil, time.Now())
		if version != step.version || changed != step.changed {
			t.Errorf("step %d: Register() = %d, %t; want %d, %t", i, version, changed, step.version, step.changed)
		}
	}

	v, err := s.Version(t.Context(), "default", "ci", 2)
	if err != nil || string(v.Source) != ciChanged || v.Fingerprint != jobs.Fingerprint([]byte(ciChanged)) {
		t.Errorf("Version(2) = %+v, %v; want the changed source", v, err)
	}

	versions, err := s.Versions(t.Context(), "default", "ci")
	if err != nil || len(versions) != 2 || versions[0].Version != 2 || versions[0].Source != nil {
		t.Errorf("Versions() = %+v, %v; want 2 then 1, without source", versions, err)
	}
}

// stopThenRevive: a stopped job stays at its version, and registering it
// again, unchanged, makes a new version and leaves it running.
func stopThenRevive(t *testing.T, s JobStore) {
	registerCI(t, s, ciSource, nil, time.Now())
	stopCI(t, s)

	if j := readCI(t, s); !j.Stopped || j.Version != 1 {
		t.Fatalf("Job() = %+v, want stopped at version 1", j)
	}

	if version, changed := registerCI(t, s, ciSource, nil, time.Now()); version != 2 || !changed {
		t.Errorf("Register() after a stop = %d, %t; want 2, true", version, changed)
	}

	if readCI(t, s).Stopped {
		t.Error("registering again left the job stopped")
	}
}

// namespacesAndMissing: a same-named job in another namespace is not listed
// in default, and reading or stopping a job nobody registered is not found.
func namespacesAndMissing(t *testing.T, s JobStore) {
	registerCI(t, s, ciSource, nil, time.Now())

	if _, _, err := s.Register(t.Context(), "research", "ci", []byte(ciChanged), nil, time.Now()); err != nil {
		t.Fatalf("Register() in research = %v", err)
	}

	listed, err := s.Jobs(t.Context(), "default")
	if err != nil || len(listed) != 1 || listed[0].Version != 1 {
		t.Errorf("Jobs(default) = %+v, %v; want ci at version 1 only", listed, err)
	}

	if _, err := s.Job(t.Context(), "default", "absent"); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("Job(absent) = %v, want ErrNotFound", err)
	}

	if err := s.Stop(t.Context(), "default", "absent", time.Now()); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("Stop(absent) = %v, want ErrNotFound", err)
	}
}

// periodicListed: of a periodic job, a job with no block and one whose block
// is disabled, only the first is listed, with the schedule kept for it and its
// registration as its last launch. Reading the others shows no schedule.
func periodicListed(t *testing.T, s JobStore) {
	registered := instant()

	registerCI(t, s, ciSource, daily, registered)

	if _, _, err := s.Register(t.Context(), "default", "plain", []byte(ciSource), nil, registered); err != nil {
		t.Fatalf("Register(plain) = %v", err)
	}

	off := &job.Periodic{Crons: daily.Crons, Enabled: new(false)}
	if _, _, err := s.Register(t.Context(), "default", "off", []byte(ciSource), off, registered); err != nil {
		t.Fatalf("Register(off) = %v", err)
	}

	want := []*jobs.Job{{
		Namespace: "default", Name: "ci", Version: 1, Updated: registered,
		Periodic: dailyKept, Launched: registered,
	}}

	if diff := cmp.Diff(want, periodicIn(t, s)); diff != "" {
		t.Errorf("PeriodicJobs() (-want +got):\n%s", diff)
	}

	for _, name := range []string{"plain", "off"} {
		if j, err := s.Job(t.Context(), "default", name); err != nil || j.Periodic != nil || !j.Launched.IsZero() {
			t.Errorf("Job(%s) = %+v, %v; want no schedule", name, j, err)
		}
	}
}

// launchKept: a periodic job that launched, then registered again with a
// change, keeps the launch rather than the time of the second register.
func launchKept(t *testing.T, s JobStore) {
	registered := instant()
	launched := registered.Add(time.Hour)

	registerCI(t, s, ciSource, daily, registered)
	claimLaunch(t, s, readCI(t, s), launched, true)
	registerCI(t, s, ciChanged, daily, launched.Add(time.Hour))

	if got := readCI(t, s); got.Version != 2 || !got.Launched.Equal(launched) {
		t.Errorf("Job() = version %d launched %s, want version 2 launched %s", got.Version, got.Launched, launched)
	}
}

// launchReset: a launched periodic job, stopped and registered again, starts
// from that register; so does one disabled and then enabled again, and it is
// not listed while disabled.
func launchReset(t *testing.T, s JobStore) {
	registered := instant()

	registerCI(t, s, ciSource, daily, registered)
	claimLaunch(t, s, readCI(t, s), registered.Add(time.Hour), true)
	stopCI(t, s)

	revived := registered.Add(2 * time.Hour)
	registerCI(t, s, ciSource, daily, revived)

	if got := readCI(t, s); !got.Launched.Equal(revived) {
		t.Errorf("after a stop, Launched = %s, want %s", got.Launched, revived)
	}

	registerCI(t, s, ciChanged, &job.Periodic{Crons: daily.Crons, Enabled: new(false)}, revived.Add(time.Hour))

	if listed := periodicIn(t, s); len(listed) != 0 {
		t.Errorf("PeriodicJobs() while disabled = %+v, want none", listed)
	}

	enabled := revived.Add(2 * time.Hour)
	registerCI(t, s, ciSource, daily, enabled)

	if got := readCI(t, s); !got.Launched.Equal(enabled) {
		t.Errorf("after enabling, Launched = %s, want %s", got.Launched, enabled)
	}
}

// claimedOnce: of two claims from one read, only the first moves the launch.
// A claim from a read taken before a new version or a stop moves nothing.
func claimedOnce(t *testing.T, s JobStore) {
	registered := instant()

	registerCI(t, s, ciSource, daily, registered)

	before := readCI(t, s)
	claimLaunch(t, s, before, registered.Add(time.Hour), true)
	claimLaunch(t, s, before, registered.Add(2*time.Hour), false)

	// The launch time matches but the version is stale
	stale := readCI(t, s)
	registerCI(t, s, ciChanged, daily, registered.Add(3*time.Hour))
	claimLaunch(t, s, stale, registered.Add(4*time.Hour), false)

	current := readCI(t, s)
	stopCI(t, s)
	claimLaunch(t, s, current, registered.Add(5*time.Hour), false)

	if got := readCI(t, s); !got.Launched.Equal(registered.Add(time.Hour)) {
		t.Errorf("Launched = %s, want only the first claim's %s", got.Launched, registered.Add(time.Hour))
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// instant is a time every store keeps exactly, in UTC to the microsecond.
func instant() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// registerCI registers source as job ci in default with periodic, failing the
// test if the store refuses.
func registerCI(t *testing.T, s JobStore, source string, periodic *job.Periodic, now time.Time) (int64, bool) {
	t.Helper()

	version, changed, err := s.Register(t.Context(), "default", "ci", []byte(source), periodic, now)
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	return version, changed
}

// stopCI stops job ci in default, failing the test if the store refuses.
func stopCI(t *testing.T, s JobStore) {
	t.Helper()

	if err := s.Stop(t.Context(), "default", "ci", time.Now()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
}

// readCI reads job ci in default, failing the test if it cannot.
func readCI(t *testing.T, s JobStore) *jobs.Job {
	t.Helper()

	j, err := s.Job(t.Context(), "default", "ci")
	if err != nil {
		t.Fatalf("Job() = %v", err)
	}

	return j
}

// claimLaunch claims the launch after j at now, failing the test on an error
// or a claim that did not go as want says.
func claimLaunch(t *testing.T, s JobStore, j *jobs.Job, now time.Time, want bool) {
	t.Helper()

	claimed, err := s.ClaimLaunch(t.Context(), j, now)
	if err != nil || claimed != want {
		t.Fatalf("ClaimLaunch(launched %s, version %d) = %t, %v; want %t", j.Launched, j.Version, claimed, err, want)
	}
}

// periodicIn lists the store's periodic jobs, failing the test if it cannot.
func periodicIn(t *testing.T, s JobStore) []*jobs.Job {
	t.Helper()

	out, err := s.PeriodicJobs(t.Context())
	if err != nil {
		t.Fatalf("PeriodicJobs() = %v", err)
	}

	return out
}
