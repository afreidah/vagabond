// -------------------------------------------------------------------------------
// Memory Job Store Tests
//
// Author: Alex Freidah
// -------------------------------------------------------------------------------

package memory

import (
	"errors"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/jobs"
)

// A version is made only on a change, reformatting is not one, and a stopped
// job is revived by registering it again.
func TestJobs_RegisterVersionsOnChange(t *testing.T) {
	s := NewJobs(NewExecutions())
	ctx := t.Context()
	now := time.Now()

	steps := []struct {
		name    string
		source  string
		stop    bool
		version int64
		changed bool
	}{
		{"first", `job "ci" {}`, false, 1, true},
		{"reformatted", `job   "ci"   {}`, false, 1, false},
		{"changed", `job "ci" { type = "batch" }`, false, 2, true},
		{"after stop", `job "ci" { type = "batch" }`, true, 3, true},
	}

	for _, step := range steps {
		if step.stop {
			if err := s.Stop(ctx, "default", "ci", now); err != nil {
				t.Fatalf("%s: Stop() = %v", step.name, err)
			}
		}

		version, changed, err := s.Register(ctx, "default", "ci", []byte(step.source), now)
		if err != nil || version != step.version || changed != step.changed {
			t.Errorf("%s: Register() = %d, %t, %v; want %d, %t", step.name, version, changed, err, step.version, step.changed)
		}
	}

	versions, _ := s.Versions(ctx, "default", "ci")
	if len(versions) != 3 || versions[0].Version != 3 || versions[0].Source != nil {
		t.Errorf("Versions() = %+v, want 3 newest first without source", versions)
	}

	v, err := s.Version(ctx, "default", "ci", 2)
	if err != nil || string(v.Source) != `job "ci" { type = "batch" }` {
		t.Errorf("Version(2) = %+v, %v", v, err)
	}
}

// Stopping or reading a job nobody registered is not found.
func TestJobs_Unknown(t *testing.T) {
	s := NewJobs(NewExecutions())

	if _, err := s.Job(t.Context(), "default", "ci"); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("Job() = %v, want ErrNotFound", err)
	}

	if err := s.Stop(t.Context(), "default", "ci", time.Now()); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("Stop() = %v, want ErrNotFound", err)
	}
}
