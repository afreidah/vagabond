//go:build integration

// -------------------------------------------------------------------------------
// Registered Job Store Integration Tests
//
// Author: Alex Freidah
//
// Every case runs against Postgres and CockroachDB, using the engines and open
// helper from the store suite.
// -------------------------------------------------------------------------------

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/state/postgres"
)

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

// mustRegister registers source as job ci, failing the test on an error.
func mustRegister(ctx context.Context, t *testing.T, s *postgres.Store, source string) (int64, bool) {
	t.Helper()

	version, changed, err := s.Register(ctx, "default", "ci", []byte(source), time.Now())
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	return version, changed
}

// -------------------------------------------------------------------------
// REGISTRY
// -------------------------------------------------------------------------

// A version is made only when the job changed, and formatting alone is not a
// change.
func TestJobs_RegisterVersionsOnlyOnChange(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			steps := []struct {
				source      string
				wantVersion int64
				wantChanged bool
			}{
				{ciSource, 1, true},
				{ciSource, 1, false},
				{ciReformatted, 1, false},
				{ciChanged, 2, true},
			}

			for i, step := range steps {
				version, changed := mustRegister(ctx, t, store, step.source)

				if version != step.wantVersion || changed != step.wantChanged {
					t.Errorf("step %d: version %d changed %v, want %d %v",
						i, version, changed, step.wantVersion, step.wantChanged)
				}
			}

			v, err := store.Version(ctx, "default", "ci", 2)
			if err != nil {
				t.Fatalf("Version() = %v", err)
			}

			if string(v.Source) != ciChanged || v.Fingerprint != jobs.Fingerprint([]byte(ciChanged)) {
				t.Errorf("version 2 = %+v, want the changed source", v)
			}

			versions, err := store.Versions(ctx, "default", "ci")
			if err != nil || len(versions) != 2 || versions[0].Version != 2 {
				t.Errorf("Versions() = %v, %v; want 2 then 1", versions, err)
			}
		})
	}
}

// Stopping keeps the job and its versions; registering it again, unchanged,
// revives it as a new version.
func TestJobs_StopThenRevive(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			mustRegister(ctx, t, store, ciSource)

			if err := store.Stop(ctx, "default", "ci", time.Now()); err != nil {
				t.Fatalf("Stop() = %v", err)
			}

			j, err := store.Job(ctx, "default", "ci")
			if err != nil || !j.Stopped || j.Version != 1 {
				t.Fatalf("Job() = %+v, %v; want stopped at version 1", j, err)
			}

			if version, changed := mustRegister(ctx, t, store, ciSource); version != 2 || !changed {
				t.Errorf("re-register = version %d changed %v, want 2 true", version, changed)
			}

			if j, _ := store.Job(ctx, "default", "ci"); j.Stopped {
				t.Error("re-registering left the job stopped")
			}
		})
	}
}

// Namespaces keep same-named jobs apart, and missing jobs are reported as such.
func TestJobs_NamespacesAndMissing(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			store := open(ctx, t, e)

			mustRegister(ctx, t, store, ciSource)

			if _, _, err := store.Register(ctx, "research", "ci", []byte(ciChanged), time.Now()); err != nil {
				t.Fatalf("Register() in research = %v", err)
			}

			listed, err := store.Jobs(ctx, "default")
			if err != nil || len(listed) != 1 || listed[0].Version != 1 {
				t.Errorf("Jobs(default) = %v, %v; want ci at version 1 only", listed, err)
			}

			if _, err := store.Job(ctx, "default", "absent"); !errors.Is(err, jobs.ErrNotFound) {
				t.Errorf("Job(absent) = %v, want ErrNotFound", err)
			}

			if err := store.Stop(ctx, "default", "absent", time.Now()); !errors.Is(err, jobs.ErrNotFound) {
				t.Errorf("Stop(absent) = %v, want ErrNotFound", err)
			}
		})
	}
}
