// -------------------------------------------------------------------------------
// Quota Snapshot Tests
//
// Author: Alex Freidah
//
// The zero value gets explicit coverage because it is what a provider nothing
// has been observed about looks like, and reading it as "plenty of allowance"
// is the mistake that spends money.
// -------------------------------------------------------------------------------

package quota

import (
	"testing"
	"time"
)

func TestSnapshot_HasHeadroom(t *testing.T) {
	observed := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		input Snapshot
		want  bool
	}{
		{
			name:  "observed with allowance left",
			input: Snapshot{Exhausted: false, FreePercent: 60, ObservedAt: observed},
			want:  true,
		},
		{
			name:  "observed and exhausted",
			input: Snapshot{Exhausted: true, ObservedAt: observed},
			want:  false,
		},
		{
			name:  "observed with nothing left but not flagged",
			input: Snapshot{Exhausted: false, FreePercent: 0, ObservedAt: observed},
			want:  true,
		},
		{
			name:  "never observed",
			input: Snapshot{Exhausted: false, FreePercent: 100},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.HasHeadroom(); got != tt.want {
				t.Errorf("HasHeadroom() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A provider nothing is known about must not read as one with capacity. The
// zero value claiming headroom is the failure that spends money.
func TestSnapshot_ZeroValueHasNoHeadroom(t *testing.T) {
	var s Snapshot

	if s.HasHeadroom() {
		t.Error("the zero-value snapshot claims free-tier headroom")
	}
}

func TestSnapshot_Expired(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		input Snapshot
		want  bool
	}{
		{
			name:  "period still open",
			input: Snapshot{PeriodEnds: now.Add(time.Hour)},
			want:  false,
		},
		{
			name:  "period just ended",
			input: Snapshot{PeriodEnds: now.Add(-time.Second)},
			want:  true,
		},
		{
			name:  "no period recorded",
			input: Snapshot{},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Expired(now); got != tt.want {
				t.Errorf("Expired() = %v, want %v", got, tt.want)
			}
		})
	}
}
