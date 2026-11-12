// -------------------------------------------------------------------------------
// Platform Timestamp Tests
//
// Author: Alex Freidah
//
// A time the platform has not reported, or reported in a form Vagabond cannot
// read, is zero rather than an error or a guess.
// -------------------------------------------------------------------------------

package plugin

import (
	"testing"
	"time"
)

// Whole and fractional seconds and any offset parse; anything else is zero.
func TestParseTime(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 11, 11, 22, 0, 0, 0, time.UTC)

	cases := []struct {
		value string
		want  time.Time
	}{
		{"2026-11-11T22:00:00Z", want},
		{"2026-11-11T22:00:00.250Z", want.Add(250 * time.Millisecond)},
		{"2026-11-11T14:00:00-08:00", want},
		{"", time.Time{}},
		{"yesterday", time.Time{}},
	}

	for _, tc := range cases {
		if got := ParseTime(tc.value); !got.Equal(tc.want) {
			t.Errorf("ParseTime(%q) = %s, want %s", tc.value, got, tc.want)
		}
	}
}
