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

// Limits is the largest task a provider will accept. A nil field advertises no
// limit; zero is a real limit that admits nothing, which a full pool node
// needs to say.
//
// A provider that does not let a caller choose CPU, as Lambda does not,
// advertises the ceiling its largest memory tier implies rather than leaving
// it nil.
type Limits struct {
	CPU    *int // millicores
	Memory *int // MiB
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

	MaxResources Limits
	MaxDuration  time.Duration // zero means the provider advertised no limit

	// DefaultResources is what the provider sizes a task that declares no CPU or
	// memory at, so admission checks the size it will run at. Zero where the
	// provider sets no size of its own.
	DefaultResources Resources

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

// WithinDuration reports whether the provider will run a task for d.
//
// A zero MaxDuration means no limit was advertised, not a limit of zero.
// Container providers genuinely have no meaningful bound, so the permissive
// reading is the useful one.
func (c *Capabilities) WithinDuration(d time.Duration) bool {
	return c.MaxDuration == 0 || d <= c.MaxDuration
}

// Sized returns r with the provider's defaults in place of anything r leaves
// zero: the size a task will actually run at here.
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
