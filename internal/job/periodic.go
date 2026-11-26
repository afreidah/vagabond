// -------------------------------------------------------------------------------
// Periodic - launching a registered job on a schedule
//
// Author: Alex Freidah
//
// A periodic block lists cron expressions and the time zone they are read in.
// Each cron is five fields or one of the standard macros, so every launch falls
// on a whole minute. Crons follow the zone's clock, and a launch waits for the
// clock to show a matching time later than any it has shown before. When
// clocks go back, the repeated hour launches nothing; when they go forward, a
// time inside the skipped hour launches as the clock jumps past it.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	// The zone database is built in, so a time zone resolves on a host or
	// image that has none installed.
	_ "time/tzdata"

	"github.com/hashicorp/cronexpr"

	"github.com/afreidah/vagabond/internal/ptr"
)

// -------------------------------------------------------------------------
// PERIODIC
// -------------------------------------------------------------------------

// Periodic launches a registered job on a cron schedule. TimeZone is nil when
// the block names none, which means UTC. Enabled is nil when the block leaves
// it out, which means launches are on.
type Periodic struct {
	Crons           []string `hcl:"crons"`
	TimeZone        *string  `hcl:"time_zone,optional"`
	ProhibitOverlap *bool    `hcl:"prohibit_overlap,optional"`
	Enabled         *bool    `hcl:"enabled,optional"`
}

// ErrInvalidSchedule is the sentinel behind every cron or time zone a periodic
// block cannot launch by.
var ErrInvalidSchedule = errors.New("invalid schedule")

// macros are the cron shorthands Vagabond accepts. Every cron implementation
// reads them the same way, and each one falls on a whole minute.
var macros = []string{"@yearly", "@annually", "@monthly", "@weekly", "@daily", "@hourly"}

// Active reports whether the block launches anything: it exists, and enabled
// is not set to false. It is safe to call on a job with no periodic block.
func (p *Periodic) Active() bool {
	return p != nil && ptr.DerefOr(p.Enabled, true)
}

// Location returns the time zone the crons are read in, UTC when the block
// names none. An empty name and Local are refused, since servers sharing a
// store can sit in different zones.
func (p *Periodic) Location() (*time.Location, error) {
	name := ptr.DerefOr(p.TimeZone, "UTC")
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("%w: time zone %q is not a named zone", ErrInvalidSchedule, name)
	}

	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: unknown time zone %q", ErrInvalidSchedule, name)
	}

	return loc, nil
}

// Next returns when the launch after last falls due: the first moment the
// clock in the block's time zone shows a time any cron matches, later than
// every time it had shown by last. It fails on a time zone or cron the block
// cannot launch by.
func (p *Periodic) Next(last time.Time) (time.Time, error) {
	loc, err := p.Location()
	if err != nil {
		return time.Time{}, err
	}

	from := shown(last, loc)

	var first time.Time

	for _, expr := range p.Crons {
		match, err := nextCron(expr, from)
		if err != nil {
			return time.Time{}, err
		}

		if first.IsZero() || match.Before(first) {
			first = match
		}
	}

	if first.IsZero() {
		return time.Time{}, fmt.Errorf("%w: no crons", ErrInvalidSchedule)
	}

	return reaches(first, loc), nil
}

// shown returns the latest time the clock in loc had shown by t, written as a
// UTC time. That is t's own reading, except in the hour that repeats after
// clocks go back, when the clock showed later times before it went back.
func shown(t time.Time, loc *time.Location) time.Time {
	at := t.In(loc)
	reading := clock(at)

	start, _ := at.ZoneBounds()
	if start.IsZero() {
		return reading
	}

	before := clock(start.Add(-time.Second).In(loc))
	if before.After(reading) {
		return before
	}

	return reading
}

// reaches returns the first moment the clock in loc shows w, a reading written
// as a UTC time, or later. A reading the clock shows twice gives the first
// moment; one it skips gives the moment it jumps past it.
func reaches(w time.Time, loc *time.Location) time.Time {
	// No moment before this one shows w, since no zone is more than 14 hours ahead of UTC
	t := w.Add(-14 * time.Hour)

	// Walk forward through the zone's offsets to the first that reaches w
	for {
		at := t.In(loc)
		_, offset := at.Zone()
		moment := w.Add(-time.Duration(offset) * time.Second)

		// The clock was already past w when this offset began, so it jumped over w here
		if moment.Before(t) {
			return t
		}

		_, end := at.ZoneBounds()
		if end.IsZero() || moment.Before(end) {
			return moment
		}

		t = end
	}
}

// clock returns the reading on t's clock, to the second, written as a UTC
// time so cron matching never sees a clock change.
func clock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// CheckCron reports why expr is not a cron a periodic block can launch by: it
// is not five fields or a macro, a field is malformed, or no date matches it.
func CheckCron(expr string) error {
	_, err := nextCron(expr, time.Now().UTC())

	return err
}

// nextCron returns the first reading after the given one that expr matches.
// Both are clock readings written as UTC times.
func nextCron(expr string, after time.Time) (time.Time, error) {
	fields := strings.Fields(expr)

	// A seconds or a year field would let a launch fall between minutes or stop recurring
	macro := len(fields) == 1 && slices.Contains(macros, strings.ToLower(fields[0]))
	if len(fields) != 5 && !macro {
		return time.Time{}, fmt.Errorf("%w: cron %q is neither five fields nor one of %s",
			ErrInvalidSchedule, expr, strings.Join(macros, ", "))
	}

	// cronexpr reads a macro only in lower case, and every field in any case
	line := expr
	if macro {
		line = strings.ToLower(fields[0])
	}

	cron, err := cronexpr.Parse(line)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: cron %q has a malformed or out-of-range field",
			ErrInvalidSchedule, expr)
	}

	// A valid cron has no next time only when no date matches it, such as the 30th of February
	next := cron.Next(after)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("%w: no date matches cron %q", ErrInvalidSchedule, expr)
	}

	return next, nil
}
