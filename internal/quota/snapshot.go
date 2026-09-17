// -------------------------------------------------------------------------------
// Quota Snapshot
//
// Author: Alex Freidah
//
// What admission reads about a provider's remaining free-tier allowance. A
// value, not a handle: admission is a pure function and must not reach a
// database while deciding, so the snapshot is gathered before it runs.
//
// Deliberately unitful only in percent. Providers meter free tiers in
// incompatible units, and normalizing vCPU-seconds against GB-seconds against
// request counts is a problem the ledger solves per provider. What admission
// needs is narrower: whether there is anything left, and roughly how much, so
// that scarce capacity can be preferred last.
// -------------------------------------------------------------------------------

package quota

import "time"

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Snapshot is a provider's free-tier standing at a moment in time.
//
// FreePercent is what the provider.free_quota_percent affinity matches on. It
// is deliberately coarse: an affinity steering work away from a nearly spent
// provider does not need precision, and precision is exactly what the
// underlying units cannot offer honestly.
//
// The zero value is an exhausted provider with nothing observed. That is the
// safe reading of a provider nothing is known about, because admission rejects
// what it cannot confirm rather than spending an allowance it cannot see.
type Snapshot struct {
	Provider    string
	Exhausted   bool
	FreePercent int
	PeriodEnds  time.Time
	ObservedAt  time.Time
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// HasHeadroom reports whether the provider has free-tier allowance left.
//
// A snapshot that was never observed has no headroom. An unobserved provider
// and an exhausted one are different facts, but they lead to the same decision,
// and the one that spends money is not the one to guess toward.
func (s *Snapshot) HasHeadroom() bool {
	if s.ObservedAt.IsZero() {
		return false
	}

	return !s.Exhausted
}

// Expired reports whether the period this snapshot describes has already ended
// as of now.
//
// A snapshot past its period reset understates the allowance rather than
// overstating it, so it is safe to schedule against and merely wasteful. The
// refresh loop uses this to know what to re-read first.
func (s *Snapshot) Expired(now time.Time) bool {
	if s.PeriodEnds.IsZero() {
		return false
	}

	return now.After(s.PeriodEnds)
}
