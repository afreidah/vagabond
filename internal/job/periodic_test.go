// -------------------------------------------------------------------------------
// Periodic Tests
//
// Author: Alex Freidah
//
// Pins when a periodic block launches, including both clock changes in a zone
// that has them, so a change in the cron library's behaviour fails here rather
// than as a double or missing launch.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// NEXT
// -------------------------------------------------------------------------

// Each case gives the last launch and the moment the next one falls due.
// Clock-change cases use New York, whose clocks went back on 1 November 2026
// and went forward on 8 March 2026.
func TestPeriodic_Next(t *testing.T) {
	tests := []struct {
		name  string
		crons []string
		zone  string
		last  string
		want  string
	}{
		{
			name:  "a time inside a matching minute waits for the next minute",
			crons: []string{"* * * * *"},
			last:  "2026-11-21T10:00:30Z",
			want:  "2026-11-21T10:01:00Z",
		},
		{
			name:  "a launched minute is not launched again",
			crons: []string{"0 10 * * *"},
			last:  "2026-11-21T10:00:00Z",
			want:  "2026-11-22T10:00:00Z",
		},
		{
			name:  "the earliest cron wins",
			crons: []string{"0 12 * * *", "0 9 * * *"},
			last:  "2026-11-21T08:00:00Z",
			want:  "2026-11-21T09:00:00Z",
		},
		{
			name:  "crons are read in a zone behind UTC",
			crons: []string{"0 9 * * *"},
			zone:  "America/New_York",
			last:  "2026-11-21T12:00:00Z",
			want:  "2026-11-21T14:00:00Z",
		},
		{
			name:  "crons are read in the zone furthest ahead of UTC",
			crons: []string{"0 0 * * *"},
			zone:  "Pacific/Kiritimati",
			last:  "2026-11-21T10:00:00Z",
			want:  "2026-11-22T10:00:00Z",
		},
		{
			name:  "a day range ending on 7 includes Sunday",
			crons: []string{"0 0 * * 5-7"},
			last:  "2026-11-21T12:00:00Z",
			want:  "2026-11-22T00:00:00Z",
		},
		{
			name:  "a macro is read in either case",
			crons: []string{"@DAILY"},
			last:  "2026-11-21T10:00:00Z",
			want:  "2026-11-22T00:00:00Z",
		},
		{
			name:  "a leap-day cron waits for a leap year",
			crons: []string{"0 0 29 2 *"},
			last:  "2026-03-01T00:00:00Z",
			want:  "2028-02-29T00:00:00Z",
		},
		{
			name:  "the repeated hour does not launch a time again",
			crons: []string{"30 1 * * *"},
			zone:  "America/New_York",
			last:  "2026-11-01T01:31:00-04:00",
			want:  "2026-11-02T01:30:00-05:00",
		},
		{
			name:  "a launch made in the repeated hour counts its first pass",
			crons: []string{"30 1 * * *"},
			zone:  "America/New_York",
			last:  "2026-11-01T01:10:00-05:00",
			want:  "2026-11-02T01:30:00-05:00",
		},
		{
			name:  "the repeated hour pauses a job due every minute",
			crons: []string{"* * * * *"},
			zone:  "America/New_York",
			last:  "2026-11-01T01:59:00-04:00",
			want:  "2026-11-01T02:00:00-05:00",
		},
		{
			name:  "a time the clock shows twice falls due the first time",
			crons: []string{"45 1 * * *"},
			zone:  "America/New_York",
			last:  "2026-11-01T00:00:00-04:00",
			want:  "2026-11-01T01:45:00-04:00",
		},
		{
			name:  "a skipped time launches as the clock jumps past it",
			crons: []string{"30 2 * * *"},
			zone:  "America/New_York",
			last:  "2026-03-08T00:00:00-05:00",
			want:  "2026-03-08T03:00:00-04:00",
		},
		{
			name:  "the skipped hour launches a job due every minute once",
			crons: []string{"* * * * *"},
			zone:  "America/New_York",
			last:  "2026-03-08T01:59:00-05:00",
			want:  "2026-03-08T03:00:00-04:00",
		},
		{
			name:  "a launch after the jump waits for the next minute",
			crons: []string{"* * * * *"},
			zone:  "America/New_York",
			last:  "2026-03-08T03:00:00-04:00",
			want:  "2026-03-08T03:01:00-04:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Periodic{Crons: tt.crons}
			if tt.zone != "" {
				p.TimeZone = new(tt.zone)
			}

			got, err := p.Next(instant(t, tt.last))
			if err != nil {
				t.Fatalf("Next returned unexpected error: %v", err)
			}

			if want := instant(t, tt.want); !got.Equal(want) {
				t.Errorf("Next = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

// A block whose time zone or crons cannot be launched by fails with
// ErrInvalidSchedule instead of returning a time.
func TestPeriodic_Next_Invalid(t *testing.T) {
	tests := []struct {
		name     string
		periodic Periodic
	}{
		{name: "unknown zone", periodic: Periodic{Crons: []string{"@daily"}, TimeZone: new("Mars/Base")}},
		{name: "invalid cron", periodic: Periodic{Crons: []string{"@daily", "@5minutes"}}},
		{name: "no crons", periodic: Periodic{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.periodic.Next(time.Now()); !errors.Is(err, ErrInvalidSchedule) {
				t.Errorf("Next error = %v, want ErrInvalidSchedule", err)
			}
		})
	}
}

// -------------------------------------------------------------------------
// CRONS
// -------------------------------------------------------------------------

// Five fields and the standard macros are accepted. Seconds, years, the
// library's own macros and crons no date matches are refused.
func TestCheckCron(t *testing.T) {
	tests := []struct {
		expr string
		ok   bool
	}{
		{expr: "*/15 * * * *", ok: true},
		{expr: "0 3 * * 1-5", ok: true},
		{expr: "0 0 * * fri,sat,sun", ok: true},
		{expr: "@hourly", ok: true},
		{expr: "@Weekly", ok: true},
		{expr: "", ok: false},
		{expr: "0 0 * * * *", ok: false},
		{expr: "0 0 1 1 * 2027", ok: false},
		{expr: "@5minutes", ok: false},
		{expr: "@always", ok: false},
		{expr: "@everysecond", ok: false},
		{expr: "0 0 * * fri-sun", ok: false},
		{expr: "61 * * * *", ok: false},
		{expr: "0 0 30 2 *", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			err := CheckCron(tt.expr)
			if tt.ok && err != nil {
				t.Errorf("CheckCron(%q) = %v, want nil", tt.expr, err)
			}

			if !tt.ok && !errors.Is(err, ErrInvalidSchedule) {
				t.Errorf("CheckCron(%q) = %v, want ErrInvalidSchedule", tt.expr, err)
			}
		})
	}
}

// -------------------------------------------------------------------------
// LOCATION AND ACTIVE
// -------------------------------------------------------------------------

// A block naming no zone reads its crons in UTC, and a named zone resolves.
// An empty name and Local are refused, as is a zone that does not exist.
func TestPeriodic_Location(t *testing.T) {
	tests := []struct {
		name string
		zone *string
		want string
	}{
		{name: "default", want: "UTC"},
		{name: "named", zone: new("Europe/Berlin"), want: "Europe/Berlin"},
		{name: "empty", zone: new("")},
		{name: "local", zone: new("Local")},
		{name: "unknown", zone: new("Mars/Base")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := (&Periodic{TimeZone: tt.zone}).Location()
			if tt.want == "" {
				if !errors.Is(err, ErrInvalidSchedule) {
					t.Errorf("Location error = %v, want ErrInvalidSchedule", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("Location returned unexpected error: %v", err)
			}

			if loc.String() != tt.want {
				t.Errorf("Location = %s, want %s", loc, tt.want)
			}
		})
	}
}

// A block launches unless it is missing or sets enabled to false, and asking
// a job with no block is safe.
func TestPeriodic_Active(t *testing.T) {
	tests := []struct {
		name     string
		periodic *Periodic
		want     bool
	}{
		{name: "no block", want: false},
		{name: "enabled left out", periodic: &Periodic{}, want: true},
		{name: "enabled", periodic: &Periodic{Enabled: new(true)}, want: true},
		{name: "disabled", periodic: &Periodic{Enabled: new(false)}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.periodic.Active(); got != tt.want {
				t.Errorf("Active = %v, want %v", got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// instant parses an RFC 3339 time for a test case, failing the test when the
// case is written wrong.
func instant(t *testing.T, s string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test time %q: %v", s, err)
	}

	return parsed
}
