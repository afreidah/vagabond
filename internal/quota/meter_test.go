// -------------------------------------------------------------------------------
// Meter and Period Tests
//
// Author: Alex Freidah
//
// Rounding gets explicit coverage in both directions: up on duration, because
// under-counting is what spends money, and not at all on an execution that
// declared nothing.
// -------------------------------------------------------------------------------

package quota

import (
	"testing"
	"time"
)

func TestMeter_Valid(t *testing.T) {
	tests := []struct {
		input Meter
		want  bool
	}{
		{MeterExecutions, true},
		{MeterGBSeconds, true},
		{MeterCPUSeconds, true},
		{MeterSeconds, true},
		{Meter(""), false},
		{Meter("instance_hours"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMeter_charge(t *testing.T) {
	tests := []struct {
		name  string
		meter Meter
		input Execution
		want  int64
	}{
		{
			name:  "one per execution",
			meter: MeterExecutions,
			input: Execution{Memory: 512, Duration: 30 * time.Second},
			want:  1,
		},
		{
			name:  "an execution that declared nothing is still a request",
			meter: MeterExecutions,
			input: Execution{},
			want:  1,
		},
		{
			name:  "a gibibyte for ten seconds is ten GB-seconds",
			meter: MeterGBSeconds,
			input: Execution{Memory: 1024, Duration: 10 * time.Second},
			want:  10 * mebibytesPerGibibyte * millisPerSecond,
		},
		{
			name:  "no declared duration costs no compute",
			meter: MeterGBSeconds,
			input: Execution{Memory: 1024},
			want:  0,
		},
		{
			name:  "no declared memory costs no compute",
			meter: MeterGBSeconds,
			input: Execution{Duration: time.Minute},
			want:  0,
		},
		{
			name:  "negative memory does not credit the pool",
			meter: MeterGBSeconds,
			input: Execution{Memory: -2048, Duration: time.Second},
			want:  0,
		},
		{
			name:  "one vCPU for ten seconds is ten vCPU-seconds",
			meter: MeterCPUSeconds,
			input: Execution{CPU: 1000, Memory: 512, Duration: 10 * time.Second},
			want:  10 * millicoresPerVCPU * millisPerSecond,
		},
		{
			name:  "no declared CPU costs no CPU time",
			meter: MeterCPUSeconds,
			input: Execution{Memory: 512, Duration: time.Minute},
			want:  0,
		},
		{
			name:  "CPU and memory are metered independently",
			meter: MeterCPUSeconds,
			input: Execution{CPU: 500, Memory: 8192, Duration: 2 * time.Second},
			want:  500 * 2 * millisPerSecond,
		},
		{
			name:  "wall clock is duration alone",
			meter: MeterSeconds,
			input: Execution{CPU: 2000, Memory: 8192, Duration: 90 * time.Second},
			want:  90 * millisPerSecond,
		},
		{
			name:  "a sub-millisecond duration rounds up rather than to nothing",
			meter: MeterSeconds,
			input: Execution{Duration: 100 * time.Microsecond},
			want:  1,
		},
		{
			name:  "an unknown meter charges nothing",
			meter: Meter("instance_hours"),
			input: Execution{CPU: 1000, Memory: 1024, Duration: time.Hour},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.meter.charge(tt.input); got != tt.want {
				t.Errorf("charge() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPeriod_Valid(t *testing.T) {
	tests := []struct {
		input Period
		want  bool
	}{
		{PeriodDaily, true},
		{PeriodMonthly, true},
		{Period(""), false},
		{Period("weekly"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The key is UTC regardless of the caller's zone. An operator west of UTC late
// in the evening is already on the provider's next day, and a key that said
// otherwise would report headroom the provider does not agree exists.
func TestPeriod_Key(t *testing.T) {
	west := time.FixedZone("UTC-7", -7*60*60)
	evening := time.Date(2026, 9, 22, 18, 30, 0, 0, west) // 2026-09-23 01:30 UTC

	tests := []struct {
		name   string
		period Period
		at     time.Time
		want   string
	}{
		{
			name:   "daily",
			period: PeriodDaily,
			at:     time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC),
			want:   "2026-09-22",
		},
		{
			name:   "monthly",
			period: PeriodMonthly,
			at:     time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC),
			want:   "2026-09",
		},
		{
			name:   "daily rolls on UTC midnight, not the caller's",
			period: PeriodDaily,
			at:     evening,
			want:   "2026-09-23",
		},
		{
			name:   "monthly on the last day of the month",
			period: PeriodMonthly,
			at:     time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC),
			want:   "2026-09",
		},
		{
			name:   "an unknown period has no key",
			period: Period("weekly"),
			at:     time.Date(2026, 9, 22, 13, 0, 0, 0, time.UTC),
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.period.Key(tt.at); got != tt.want {
				t.Errorf("Key() = %q, want %q", got, tt.want)
			}
		})
	}
}
