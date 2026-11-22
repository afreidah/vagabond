// -------------------------------------------------------------------------------
// Registered Job Tests
//
// Author: Alex Freidah
//
// Covers the fingerprint that decides whether registering a job again creates
// a new version. Reformatting must not; a change to any value must. Also the
// schedule a store keeps for a periodic block.
// -------------------------------------------------------------------------------

package jobs

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/job"
)

const tidy = `job "ci" {
  type = "batch"
}
`

// Whitespace is formatting, not a change to the job.
func TestFingerprint_IgnoresFormatting(t *testing.T) {
	messy := "job   \"ci\"   {\n      type=\"batch\"\n}\n"

	if Fingerprint([]byte(tidy)) != Fingerprint([]byte(messy)) {
		t.Error("reformatting the same job changed its fingerprint")
	}
}

// A changed value is a change to the job.
func TestFingerprint_SeesAChange(t *testing.T) {
	changed := `job "ci" {
  type = "service"
}
`

	if Fingerprint([]byte(tidy)) == Fingerprint([]byte(changed)) {
		t.Error("a changed job kept its fingerprint")
	}
}

// A store keeps no schedule for a missing or disabled block, and keeps an
// active one with its defaults filled in and enabled left out.
func TestSchedule(t *testing.T) {
	crons := []string{"@daily"}

	tests := []struct {
		name     string
		periodic *job.Periodic
		want     *job.Periodic
	}{
		{name: "no block"},
		{name: "disabled", periodic: &job.Periodic{Crons: crons, Enabled: new(false)}},
		{
			name:     "defaults",
			periodic: &job.Periodic{Crons: crons, Enabled: new(true)},
			want:     &job.Periodic{Crons: crons, TimeZone: new("UTC"), ProhibitOverlap: new(false)},
		},
		{
			name:     "as written",
			periodic: &job.Periodic{Crons: crons, TimeZone: new("Europe/Berlin"), ProhibitOverlap: new(true)},
			want:     &job.Periodic{Crons: crons, TimeZone: new("Europe/Berlin"), ProhibitOverlap: new(true)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, Schedule(tt.periodic)); diff != "" {
				t.Errorf("Schedule() (-want +got):\n%s", diff)
			}
		})
	}
}
