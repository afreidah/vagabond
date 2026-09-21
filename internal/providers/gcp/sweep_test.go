// -------------------------------------------------------------------------------
// Leaked Job Cleanup Tests
//
// Author: Alex Freidah
//
// The sweep deletes things, so what it refuses to touch matters more than what
// it removes: a job belonging to someone else, or to a run still in flight,
// has to survive.
// -------------------------------------------------------------------------------

package gcp

import (
	"net/http"
	"testing"
	"time"
)

var sweepNow = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

// rfc3339 renders a time the way Google does.
func rfc3339(t time.Time) string {
	return t.Format(time.RFC3339)
}

func TestIsLeaked(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		job  runJob
		want bool
	}{
		"ours and old": {
			job: runJob{
				Name:       "projects/p/locations/l/jobs/vagabond-abc",
				CreateTime: rfc3339(sweepNow.Add(-sweepGrace - time.Hour)),
			},
			want: true,
		},
		"ours but still inside the grace period": {
			job: runJob{
				Name:       "projects/p/locations/l/jobs/vagabond-abc",
				CreateTime: rfc3339(sweepNow.Add(-time.Hour)),
			},
			want: false,
		},
		// Someone's hand-built job in the same project is not ours to delete,
		// however old it is.
		"not ours": {
			job: runJob{
				Name:       "projects/p/locations/l/jobs/nightly-backup",
				CreateTime: rfc3339(sweepNow.Add(-30 * 24 * time.Hour)),
			},
			want: false,
		},
		// Guessing wrong here destroys a running execution; leaving one behind
		// costs a row in a quota nobody is near.
		"ours but undateable": {
			job:  runJob{Name: "projects/p/locations/l/jobs/vagabond-abc"},
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, p := newFakeGoogle(t)

			if got := p.isLeaked(&tc.job, sweepNow); got != tc.want {
				t.Errorf("isLeaked = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSweepDeletesOnlyLeakedJobs(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond("GET /v2/projects/test-project/locations/us-central1/jobs",
		map[string]any{"jobs": []any{
			map[string]any{
				"name":       "projects/p/locations/l/jobs/vagabond-old",
				"createTime": rfc3339(sweepNow.Add(-48 * time.Hour)),
			},
			map[string]any{
				"name":       "projects/p/locations/l/jobs/vagabond-new",
				"createTime": rfc3339(sweepNow.Add(-time.Minute)),
			},
			map[string]any{
				"name":       "projects/p/locations/l/jobs/someone-elses",
				"createTime": rfc3339(sweepNow.Add(-48 * time.Hour)),
			},
		}})
	g.respond("DELETE /v2/projects/p/locations/l/jobs/vagabond-old", map[string]any{})

	swept, err := p.Sweep(t.Context(), sweepNow)
	if err != nil {
		t.Fatalf("Sweep failed: %v", err)
	}

	if swept != 1 {
		t.Errorf("swept %d jobs, want 1", swept)
	}

	// Exactly one delete, and it named the leaked job.
	var deleted []string

	for _, r := range g.requests {
		if r.method == http.MethodDelete {
			deleted = append(deleted, r.path)
		}
	}

	if len(deleted) != 1 || deleted[0] != "/v2/projects/p/locations/l/jobs/vagabond-old" {
		t.Errorf("deleted %v", deleted)
	}
}

// One job that cannot be deleted must not leave the rest leaked.
func TestSweepContinuesPastAFailure(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond("GET /v2/projects/test-project/locations/us-central1/jobs",
		map[string]any{"jobs": []any{
			map[string]any{
				"name":       "projects/p/locations/l/jobs/vagabond-a",
				"createTime": rfc3339(sweepNow.Add(-48 * time.Hour)),
			},
			map[string]any{
				"name":       "projects/p/locations/l/jobs/vagabond-b",
				"createTime": rfc3339(sweepNow.Add(-48 * time.Hour)),
			},
		}})
	// Only the second is deletable; the first 404s, which counts as gone.
	g.respond("DELETE /v2/projects/p/locations/l/jobs/vagabond-b", map[string]any{})

	swept, err := p.Sweep(t.Context(), sweepNow)
	if err != nil {
		t.Fatalf("Sweep failed: %v", err)
	}

	// A job Google says is not there is one fewer leak, so both count.
	if swept != 2 {
		t.Errorf("swept %d jobs, want 2", swept)
	}
}
