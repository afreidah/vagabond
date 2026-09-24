// -------------------------------------------------------------------------------
// Meters and Periods
//
// Author: Alex Freidah
//
// What an execution consumes, and when the allowance it consumed resets. The
// set of meters is closed because `job plan` prices a job without dispatching
// it, so a charge has to be arithmetic over what the task declared.
// -------------------------------------------------------------------------------

package quota

import (
	"math"
	"time"
)

// -------------------------------------------------------------------------
// METERS
// -------------------------------------------------------------------------

// Meter is what a pool counts. It fixes the arithmetic too: a pool metering
// gb_seconds is charged memory times duration and cannot be charged otherwise.
type Meter string

const (
	MeterExecutions Meter = "executions"  // one per execution
	MeterGBSeconds  Meter = "gb_seconds"  // declared memory times declared duration
	MeterCPUSeconds Meter = "cpu_seconds" // declared CPU times declared duration
	MeterSeconds    Meter = "seconds"     // declared duration alone
)

// Counters are int64 in a meter's base unit, not float64 in its natural one: a
// gibibyte-second is fractional, and fractions accumulated over a month drift.
const (
	mebibytesPerGibibyte = 1024
	millicoresPerVCPU    = 1000
	millisPerSecond      = 1000
)

// Meters returns every meter, for diagnostics that list what an operator could
// have written instead.
func Meters() []Meter {
	return []Meter{MeterExecutions, MeterGBSeconds, MeterCPUSeconds, MeterSeconds}
}

// Valid reports whether the meter is one this package can charge.
func (m Meter) Valid() bool {
	switch m {
	case MeterExecutions, MeterGBSeconds, MeterCPUSeconds, MeterSeconds:
		return true
	default:
		return false
	}
}

// Unit names the unit an operator writes a limit in, for diagnostics and usage
// reporting.
func (m Meter) Unit() string {
	switch m {
	case MeterExecutions:
		return "executions"
	case MeterGBSeconds:
		return "GB-seconds"
	case MeterCPUSeconds:
		return "vCPU-seconds"
	case MeterSeconds:
		return "seconds"
	default:
		return string(m)
	}
}

// Natural converts base units back to the unit an operator wrote, for display.
// Fractional, because half a GB-second is worth reading as such.
func (m Meter) Natural(base int64) float64 {
	scale := m.scale()
	if scale <= 0 {
		return 0
	}

	return float64(base) / float64(scale)
}

// scale converts a limit from the unit an operator writes to the base unit
// counters accumulate in. Zero for an unknown meter, which NewLimits rejects.
func (m Meter) scale() int64 {
	switch m {
	case MeterExecutions:
		return 1
	case MeterGBSeconds:
		return mebibytesPerGibibyte * millisPerSecond // MiB-milliseconds
	case MeterCPUSeconds:
		return millicoresPerVCPU * millisPerSecond // millicore-milliseconds
	case MeterSeconds:
		return millisPerSecond // milliseconds
	default:
		return 0
	}
}

// -------------------------------------------------------------------------
// PERIODS
// -------------------------------------------------------------------------

// Period is when a pool's allowance resets. Both are UTC, matching the
// providers: Cloudflare's daily budget rolls at UTC midnight wherever the
// operator is.
type Period string

const (
	PeriodDaily   Period = "daily"
	PeriodMonthly Period = "monthly"
)

const (
	dailyKeyLayout   = "2006-01-02"
	monthlyKeyLayout = "2006-01"
)

// Periods returns every period, for diagnostics that list what an operator
// could have written instead.
func Periods() []Period {
	return []Period{PeriodDaily, PeriodMonthly}
}

// Valid reports whether the period is one this package can key.
func (p Period) Valid() bool {
	switch p {
	case PeriodDaily, PeriodMonthly:
		return true
	default:
		return false
	}
}

// Key returns the calendar key counters for this period are stored under.
// Keying by calendar is what makes rollover free: a new period is a key nothing
// has written to yet.
func (p Period) Key(t time.Time) string {
	switch p {
	case PeriodDaily:
		return t.UTC().Format(dailyKeyLayout)
	case PeriodMonthly:
		return t.UTC().Format(monthlyKeyLayout)
	default:
		return ""
	}
}

// -------------------------------------------------------------------------
// CHARGING
// -------------------------------------------------------------------------

// Execution is what charging reads: what a task declared, not what it used.
type Execution struct {
	CPU      int // millicores, matching job.Resources
	Memory   int // MiB, matching job.Resources
	Duration time.Duration
}

// charge reports what e costs this meter, in base units. An execution with no
// declared duration still costs one on MeterExecutions.
func (m Meter) charge(e Execution) int64 {
	switch m {
	case MeterExecutions:
		return 1
	case MeterGBSeconds:
		return int64(max(e.Memory, 0)) * e.millis()
	case MeterCPUSeconds:
		return int64(max(e.CPU, 0)) * e.millis()
	case MeterSeconds:
		return e.millis()
	default:
		return 0
	}
}

// millis is the declared duration in whole milliseconds, rounded up. Up,
// because over-counting wastes free capacity and under-counting spends money.
func (e Execution) millis() int64 {
	if e.Duration <= 0 {
		return 0
	}

	return int64(math.Ceil(float64(e.Duration) / float64(time.Millisecond)))
}
