// -------------------------------------------------------------------------------
// Capabilities - what a provider advertises about itself
//
// Author: Alex Freidah
//
// Admission decides whether a provider could run a task without calling that
// provider, so everything admission needs has to be expressible here as data.
// That constraint drives the design: these are values, observed earlier and
// cached, never handles that reach the network when read.
//
// A field earns its place here only if admission branches on it. A platform
// limit nothing in a job maps to belongs to the plugin that knows it, and an
// operator's preference belongs in configuration.
// -------------------------------------------------------------------------------

package plugin

import (
	"slices"
	"time"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Resources is the largest task a provider will accept.
//
// It mirrors job.Resources so the comparison reads directly, but does not reuse
// it: that type uses pointers because an omitted HCL field has to stay
// distinguishable from a zero one, and nothing here is optional in that sense.
// Zero means the provider advertised no limit.
type Resources struct {
	CPU    int // MHz
	Memory int // MiB
}

// Capabilities is what a provider advertises so that admission can decide
// without contacting it.
//
// ObservedAt is what makes staleness visible. Without it a provider that has
// been unreachable for an hour is indistinguishable from one answering
// normally, and admission has no way to decline to decide on old data.
//
// The zero value advertises nothing: no drivers, no architectures, no limits.
// That is the correct reading of a provider nothing is known about, because
// admission rejects what it cannot confirm rather than assuming capacity that
// may not exist.
//
// EstimatedCost is zero for every provider Vagabond currently speaks to, and
// the field exists anyway. A job declaring max_cost_usd = 0 is making the
// promise this project exists to keep, and a promise checked against a number
// nobody publishes is not checked at all.
//
// Methods take a pointer receiver. A value receiver would copy the struct on
// every call without buying immutability in exchange, since copying it copies
// the slice headers while sharing their backing arrays. Callers that need an
// independent copy take one with Clone.
type Capabilities struct {
	Drivers       []job.DriverName
	Architectures []job.Arch

	MaxResources Resources
	MaxDuration  time.Duration // zero means the provider advertised no limit

	InternetEgress  bool
	PrivateNetwork  bool
	ArbitraryImages bool

	EstimatedCost job.Cost // one execution, once free-tier no longer covers it

	ObservedAt time.Time
}

// Clone returns a copy that shares nothing with the original.
//
// The slice fields are what make this necessary. Assigning a Capabilities
// copies their headers and leaves both values pointing at one backing array, so
// a caller that sorted or appended to Drivers would change what every other
// holder of that snapshot sees. Admission runs against a shared cached value
// across every candidate, which is exactly where that would be found late and
// be hard to attribute.
func (c *Capabilities) Clone() Capabilities {
	out := *c
	out.Drivers = slices.Clone(c.Drivers)
	out.Architectures = slices.Clone(c.Architectures)

	return out
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// SupportsDriver reports whether the provider satisfies the given execution
// contract.
func (c *Capabilities) SupportsDriver(d job.DriverName) bool {
	return slices.Contains(c.Drivers, d)
}

// SupportsArch reports whether the provider offers the given architecture.
func (c *Capabilities) SupportsArch(a job.Arch) bool {
	return slices.Contains(c.Architectures, a)
}

// WithinDuration reports whether the provider will run a task for d.
//
// A zero MaxDuration means no limit was advertised, not a limit of zero.
// Container providers genuinely have no meaningful bound, so the permissive
// reading is the useful one.
func (c *Capabilities) WithinDuration(d time.Duration) bool {
	return c.MaxDuration == 0 || d <= c.MaxDuration
}

// WithinResources reports whether the provider accepts the requested CPU and
// memory. A zero limit means none was advertised.
func (c *Capabilities) WithinResources(r Resources) bool {
	if c.MaxResources.CPU != 0 && r.CPU > c.MaxResources.CPU {
		return false
	}

	return c.MaxResources.Memory == 0 || r.Memory <= c.MaxResources.Memory
}

// StaleAt reports whether the observation is older than maxAge as of now.
//
// An unset ObservedAt counts as stale. A snapshot that never recorded when it
// was gathered is one nothing is known about, and treating it as current would
// admit against data of unknown age.
func (c *Capabilities) StaleAt(now time.Time, maxAge time.Duration) bool {
	if c.ObservedAt.IsZero() {
		return true
	}

	return now.Sub(c.ObservedAt) > maxAge
}
