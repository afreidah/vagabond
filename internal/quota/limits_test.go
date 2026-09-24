// -------------------------------------------------------------------------------
// Compiled Pool Tests
//
// Author: Alex Freidah
//
// The additive case is the one worth proving: a provider with a daily cap
// inside a monthly one must be refused by whichever runs out first, and a pool
// an execution never touches must not be able to refuse it.
// -------------------------------------------------------------------------------

package quota

import (
	"math"
	"strings"
	"testing"
	"time"
)

// One GB-second and one vCPU-second in base units, for readable expectations.
const (
	gbSeconds  = mebibytesPerGibibyte * millisPerSecond
	cpuSeconds = millicoresPerVCPU * millisPerSecond
)

func TestNewLimits_Rejects(t *testing.T) {
	tests := []struct {
		name  string
		input []PoolSpec
		want  string
	}{
		{
			name:  "no name",
			input: []PoolSpec{{Meter: MeterExecutions, Limit: 1, Period: PeriodMonthly}},
			want:  "has no name",
		},
		{
			name: "duplicate name",
			input: []PoolSpec{
				{Name: "requests", Meter: MeterExecutions, Limit: 1, Period: PeriodMonthly},
				{Name: "requests", Meter: MeterExecutions, Limit: 2, Period: PeriodDaily},
			},
			want: "declared twice",
		},
		{
			name:  "unknown meter",
			input: []PoolSpec{{Name: "cpu", Meter: Meter("vcpu_seconds"), Limit: 1, Period: PeriodMonthly}},
			want:  "unknown",
		},
		{
			name:  "unknown period",
			input: []PoolSpec{{Name: "requests", Meter: MeterExecutions, Limit: 1, Period: Period("weekly")}},
			want:  "unknown period",
		},
		{
			name:  "negative limit",
			input: []PoolSpec{{Name: "requests", Meter: MeterExecutions, Limit: -1, Period: PeriodMonthly}},
			want:  "no positive limit",
		},
		{
			name:  "no limit at all",
			input: []PoolSpec{{Name: "requests", Meter: MeterExecutions, Period: PeriodMonthly}},
			want:  "no positive limit",
		},
		{
			name:  "limit too large to count",
			input: []PoolSpec{{Name: "compute", Meter: MeterGBSeconds, Limit: math.MaxInt64, Period: PeriodMonthly}},
			want:  "too large to count",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewLimits(tt.input)
			if err == nil {
				t.Fatalf("NewLimits() accepted %s", tt.name)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("NewLimits() error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// An operator writes GB-seconds; counters accumulate MiB-milliseconds. The
// conversion happens once, here, rather than at every comparison.
func TestNewLimits_ScalesToBaseUnits(t *testing.T) {
	limits, err := NewLimits([]PoolSpec{
		{Name: "requests", Meter: MeterExecutions, Limit: 1_000_000, Period: PeriodMonthly},
		{Name: "compute", Meter: MeterGBSeconds, Limit: 400_000, Period: PeriodMonthly},
		{Name: "cpu", Meter: MeterCPUSeconds, Limit: 180_000, Period: PeriodMonthly},
		{Name: "runtime", Meter: MeterSeconds, Limit: 3_600, Period: PeriodDaily},
	})
	if err != nil {
		t.Fatalf("NewLimits() = %v", err)
	}

	want := map[string]int64{
		"requests": 1_000_000,
		"compute":  400_000 * gbSeconds,
		"cpu":      180_000 * cpuSeconds,
		"runtime":  3_600 * millisPerSecond,
	}

	for _, p := range limits.Pools() {
		if p.Limit != want[p.Name] {
			t.Errorf("pool %q limit = %d, want %d", p.Name, p.Limit, want[p.Name])
		}
	}
}

// Configured order is preserved, because the ledger reports pools in the order
// an operator wrote them.
func TestNewLimits_PreservesOrder(t *testing.T) {
	limits, err := NewLimits([]PoolSpec{
		{Name: "compute", Meter: MeterGBSeconds, Limit: 1, Period: PeriodMonthly},
		{Name: "requests", Meter: MeterExecutions, Limit: 1, Period: PeriodMonthly},
	})
	if err != nil {
		t.Fatalf("NewLimits() = %v", err)
	}

	got := []string{limits.Pools()[0].Name, limits.Pools()[1].Name}
	if got[0] != "compute" || got[1] != "requests" {
		t.Errorf("Pools() order = %v, want [compute requests]", got)
	}
}

func TestLimits_Unlimited(t *testing.T) {
	tests := []struct {
		name  string
		input []PoolSpec
		want  bool
	}{
		{
			name:  "no pools declared",
			input: nil,
			want:  true,
		},
		{
			name:  "one pool declared",
			input: []PoolSpec{{Name: "compute", Meter: MeterGBSeconds, Limit: 400_000, Period: PeriodMonthly}},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits, err := NewLimits(tt.input)
			if err != nil {
				t.Fatalf("NewLimits() = %v", err)
			}

			if got := limits.Unlimited(); got != tt.want {
				t.Errorf("Unlimited() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A provider an operator declared no quotas for enforces nothing, rather than
// refusing everything.
func TestLimits_ZeroValueEnforcesNothing(t *testing.T) {
	var limits Limits

	if !limits.Unlimited() {
		t.Error("the zero-value Limits claims to enforce something")
	}

	if !limits.Within(nil, Execution{Memory: 8192, Duration: time.Hour}) {
		t.Error("the zero-value Limits refused an execution")
	}
}

func TestLimits_Deltas(t *testing.T) {
	limits := FixtureFunction()

	deltas := limits.Deltas(Execution{Memory: 1024, Duration: 10 * time.Second})

	if got := deltas["requests"]; got != 1 {
		t.Errorf("requests delta = %d, want 1", got)
	}

	if got := deltas["compute"]; got != 10*gbSeconds {
		t.Errorf("compute delta = %d, want %d", got, 10*gbSeconds)
	}
}

// A pool the execution costs nothing against is absent rather than present at
// zero, so the ledger has nothing to add for it.
func TestLimits_DeltasOmitsUntouchedPools(t *testing.T) {
	limits := FixtureFunction()

	deltas := limits.Deltas(Execution{Memory: 1024})

	if _, ok := deltas["compute"]; ok {
		t.Error("a zero compute charge was reported as a delta")
	}

	if got := deltas["requests"]; got != 1 {
		t.Errorf("requests delta = %d, want 1", got)
	}
}

func TestLimits_Within(t *testing.T) {
	limits := FixtureFunction()
	small := Execution{Memory: 1024, Duration: 10 * time.Second} // 1 request, 10 GB-seconds

	tests := []struct {
		name  string
		usage PoolUsage
		want  bool
	}{
		{
			name:  "nothing spent",
			usage: nil,
			want:  true,
		},
		{
			name:  "room in both pools",
			usage: PoolUsage{"requests": 500_000, "compute": 200_000 * gbSeconds},
			want:  true,
		},
		{
			name:  "landing exactly on the limit fits",
			usage: PoolUsage{"requests": 999_999, "compute": 399_990 * gbSeconds},
			want:  true,
		},
		{
			name:  "requests exhausted while compute is untouched",
			usage: PoolUsage{"requests": 1_000_000},
			want:  false,
		},
		{
			name:  "compute exhausted while requests are untouched",
			usage: PoolUsage{"compute": 400_000 * gbSeconds},
			want:  false,
		},
		{
			name:  "one GB-second short",
			usage: PoolUsage{"compute": 399_991 * gbSeconds},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := limits.Within(tt.usage, small); got != tt.want {
				t.Errorf("Within() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A refusal has to name the pool, because "no quota" and "no CPU quota, plenty
// of memory quota" are different things to an operator reading a plan.
func TestLimits_Exceeded(t *testing.T) {
	limits := FixtureFunction()
	task := Execution{Memory: 1024, Duration: 10 * time.Second}

	if p := limits.Exceeded(nil, task); p != nil {
		t.Errorf("Exceeded() named %q against an empty ledger", p.Name)
	}

	p := limits.Exceeded(PoolUsage{"compute": 400_000 * gbSeconds}, task)
	if p == nil {
		t.Fatal("Exceeded() named no pool for an execution that does not fit")
	}

	if p.Name != "compute" {
		t.Errorf("Exceeded() = %q, want compute", p.Name)
	}
}

// The number the free_quota_percent affinity reads.
func TestLimits_FreePercent(t *testing.T) {
	limits := FixtureFunction()
	task := Execution{Memory: 1024, Duration: 10 * time.Second}

	tests := []struct {
		name  string
		usage PoolUsage
		want  int
	}{
		{
			name:  "nothing spent",
			usage: nil,
			want:  100,
		},
		{
			name:  "the tightest pool wins",
			usage: PoolUsage{"requests": 100_000, "compute": 360_000 * gbSeconds},
			want:  10,
		},
		{
			name:  "an exhausted pool reports zero",
			usage: PoolUsage{"compute": 400_000 * gbSeconds},
			want:  0,
		},
		{
			name:  "a sliver left floors to zero rather than rounding to one",
			usage: PoolUsage{"compute": 399_999 * gbSeconds},
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := limits.FreePercent(tt.usage, task); got != tt.want {
				t.Errorf("FreePercent() = %d, want %d", got, tt.want)
			}
		})
	}
}

// A pool the task does not charge must not drag the number down, or a job
// declaring no duration would be steered away by a spent compute budget it
// cannot touch.
func TestLimits_FreePercentIgnoresUntouchedPools(t *testing.T) {
	limits := FixtureFunction()
	spent := PoolUsage{"compute": 400_000 * gbSeconds, "requests": 500_000}

	if got := limits.FreePercent(spent, Execution{Memory: 1024}); got != 50 {
		t.Errorf("FreePercent() = %d, want 50 from the requests pool alone", got)
	}
}

// A provider an operator declared no pools for is not scored as if it were out
// of room.
func TestLimits_FreePercentOfUnlimitedProvider(t *testing.T) {
	var limits Limits

	if got := limits.FreePercent(nil, Execution{Memory: 1024, Duration: time.Hour}); got != 100 {
		t.Errorf("FreePercent() = %d, want 100", got)
	}
}

// Base units are an implementation detail of the counters; what an operator
// reads back has to be the unit they wrote.
func TestMeter_Natural(t *testing.T) {
	tests := []struct {
		meter Meter
		base  int64
		want  float64
	}{
		{MeterExecutions, 412, 412},
		{MeterGBSeconds, 10 * gbSeconds, 10},
		{MeterGBSeconds, gbSeconds / 2, 0.5},
		{MeterCPUSeconds, 180_000 * cpuSeconds, 180_000},
		{MeterSeconds, 90 * millisPerSecond, 90},
		{Meter("instance_hours"), 5, 0},
	}

	for _, tt := range tests {
		t.Run(string(tt.meter), func(t *testing.T) {
			if got := tt.meter.Natural(tt.base); got != tt.want {
				t.Errorf("Natural(%d) = %v, want %v", tt.base, got, tt.want)
			}
		})
	}
}

// Pools are additive: two pools may meter the same thing over different
// periods, and whichever runs out first refuses the execution.
func TestLimits_WithinAdditivePools(t *testing.T) {
	limits, err := NewLimits([]PoolSpec{
		{Name: "daily-requests", Meter: MeterExecutions, Limit: 10, Period: PeriodDaily},
		{Name: "monthly-requests", Meter: MeterExecutions, Limit: 100, Period: PeriodMonthly},
	})
	if err != nil {
		t.Fatalf("NewLimits() = %v", err)
	}

	one := Execution{Memory: 128, Duration: time.Second}

	if !limits.Within(PoolUsage{"daily-requests": 9, "monthly-requests": 50}, one) {
		t.Error("Within() refused an execution both pools had room for")
	}

	if limits.Within(PoolUsage{"daily-requests": 10, "monthly-requests": 50}, one) {
		t.Error("Within() admitted an execution the daily pool had no room for")
	}

	if limits.Within(PoolUsage{"daily-requests": 0, "monthly-requests": 100}, one) {
		t.Error("Within() admitted an execution the monthly pool had no room for")
	}
}

// An exhausted pool the execution does not charge cannot refuse it. Without
// this, a spent compute budget would block a job declaring no duration.
func TestLimits_WithinIgnoresUntouchedPools(t *testing.T) {
	limits, err := NewLimits([]PoolSpec{
		{Name: "compute", Meter: MeterGBSeconds, Limit: 400_000, Period: PeriodMonthly},
	})
	if err != nil {
		t.Fatalf("NewLimits() = %v", err)
	}

	spent := PoolUsage{"compute": 400_000 * gbSeconds}

	if !limits.Within(spent, Execution{Memory: 1024}) {
		t.Error("an exhausted pool refused an execution that does not charge it")
	}
}

// The case cpu_seconds exists for. At a container platform's ordinary CPU to
// memory ratio the vCPU budget runs out first, so a provider metered only on
// GB-seconds would still be admitting work after the platform began charging.
func TestLimits_CPUBudgetBindsBeforeMemory(t *testing.T) {
	limits := FixtureContainer()

	// One vCPU against 512 MiB, the common default.
	task := Execution{CPU: 1000, Memory: 512, Duration: 10 * time.Second}

	// The vCPU budget is spent; a quarter of the GB-seconds budget is not.
	usage := PoolUsage{"cpu": 100_000 * cpuSeconds, "compute": 50_000 * gbSeconds}

	if limits.Within(usage, task) {
		t.Error("Within() admitted an execution the vCPU budget had no room for")
	}

	if !limits.Within(PoolUsage{"compute": 50_000 * gbSeconds}, task) {
		t.Error("Within() refused an execution both budgets had room for")
	}
}

// Between them the fixtures have to cover both charge kinds and both periods,
// or the ledger's tests are exercising half the vocabulary.
func TestFixtures_CoverMetersAndPeriods(t *testing.T) {
	meters := map[Meter]bool{}
	periods := map[Period]bool{}

	for _, limits := range []Limits{FixtureContainer(), FixtureFunction(), FixtureWorker()} {
		if limits.Unlimited() {
			t.Error("a fixture enforces nothing")
		}

		for _, p := range limits.Pools() {
			meters[p.Meter] = true
			periods[p.Period] = true
		}
	}

	for _, m := range []Meter{MeterExecutions, MeterGBSeconds, MeterCPUSeconds} {
		if !meters[m] {
			t.Errorf("no fixture meters %s", m)
		}
	}

	for _, p := range []Period{PeriodDaily, PeriodMonthly} {
		if !periods[p] {
			t.Errorf("no fixture resets %s", p)
		}
	}
}
