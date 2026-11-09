// -------------------------------------------------------------------------------
// Capabilities - what a provider advertises about itself
//
// Author: Alex Freidah
//
// Admission decides whether a provider could run a task without calling that
// provider, so everything admission needs is expressed here as data: values
// observed earlier and cached, never handles that reach the network when read.
// A field earns its place only if admission branches on it.
// -------------------------------------------------------------------------------

package plugin

import (
	"maps"
	"slices"
	"time"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Resources is a task size in the units of job.Resources: CPU in millicores,
// memory in MiB.
type Resources struct {
	CPU    int // millicores; 1000 is one vCPU
	Memory int // MiB
}

// Limits is the largest task a provider accepts. A nil field advertises no
// limit; zero is a real limit that admits nothing, as a full pool node says.
type Limits struct {
	CPU    *int // millicores
	Memory *int // MiB
}

// Capabilities is what a provider advertises so that admission can decide
// without contacting it.
//
// ObservedAt makes staleness visible, so admission can decline to decide on old
// data. The zero value advertises nothing, because admission rejects what it
// cannot confirm. EstimatedCost is zero for every current provider and exists
// so that max_cost_usd = 0 is checked against a published number.
//
// Methods take a pointer receiver: a value copy still shares the slices'
// backing arrays, so it buys no immutability. Clone takes a real copy.
type Capabilities struct {
	Drivers       []job.DriverName
	Architectures []job.Arch

	MaxResources Limits
	MaxDuration  time.Duration // zero means the provider advertised no limit

	DefaultResources Resources // the size of a task declaring none; zero if unset

	InternetEgress  bool
	PrivateNetwork  bool
	ArbitraryImages bool

	EstimatedCost job.Cost // one execution, once free-tier no longer covers it

	ObservedAt time.Time

	Members []Member // a pool's nodes, each judged on its own; empty otherwise
}

// Member is one node of a pool: its name, what it alone can do, and the labels
// its agent was started with.
type Member struct {
	Name         string
	Capabilities Capabilities
	Labels       map[string]string
}

// Clone returns a copy that shares nothing with the original. Assignment shares
// the slices' backing arrays, and one cached snapshot serves every candidate.
func (c *Capabilities) Clone() Capabilities {
	out := *c
	out.Drivers = slices.Clone(c.Drivers)
	out.Architectures = slices.Clone(c.Architectures)

	// The limits are pointers, so each gets its own copy.
	if c.MaxResources.CPU != nil {
		out.MaxResources.CPU = new(*c.MaxResources.CPU)
	}

	if c.MaxResources.Memory != nil {
		out.MaxResources.Memory = new(*c.MaxResources.Memory)
	}

	// Members hold slices of their own, so each is cloned in turn.
	if c.Members != nil {
		out.Members = make([]Member, len(c.Members))
		for i := range c.Members {
			out.Members[i] = Member{
				Name:         c.Members[i].Name,
				Capabilities: c.Members[i].Capabilities.Clone(),
				Labels:       maps.Clone(c.Members[i].Labels),
			}
		}
	}

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

// WithinDuration reports whether the provider runs a task for d. A zero
// MaxDuration means no limit was advertised, not a limit of zero.
func (c *Capabilities) WithinDuration(d time.Duration) bool {
	return c.MaxDuration == 0 || d <= c.MaxDuration
}

// Sized returns r with the provider's defaults in place of anything r leaves
// zero: the size a task actually runs at here.
func (c *Capabilities) Sized(r Resources) Resources {
	if r.CPU == 0 {
		r.CPU = c.DefaultResources.CPU
	}

	if r.Memory == 0 {
		r.Memory = c.DefaultResources.Memory
	}

	return r
}

// WithinResources reports whether the provider accepts a task of size r, once
// sized by its defaults. A nil limit admits anything.
func (c *Capabilities) WithinResources(r Resources) bool {
	r = c.Sized(r)

	if limit := c.MaxResources.CPU; limit != nil && r.CPU > *limit {
		return false
	}

	limit := c.MaxResources.Memory

	return limit == nil || r.Memory <= *limit
}

// StaleAt reports whether the observation is older than maxAge as of now. An
// unset ObservedAt counts as stale, since its age is unknown.
func (c *Capabilities) StaleAt(now time.Time, maxAge time.Duration) bool {
	if c.ObservedAt.IsZero() {
		return true
	}

	return now.Sub(c.ObservedAt) > maxAge
}
