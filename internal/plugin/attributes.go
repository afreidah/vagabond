// -------------------------------------------------------------------------------
// Attributes - the dotted view constraints and affinities match against
//
// Author: Alex Freidah
//
// Job files match on provider properties by dotted name, so Capabilities needs
// a string-keyed view. It is computed from the typed fields on every call
// rather than stored beside them, so the two cannot disagree.
//
// Quota-derived attributes such as provider.free_quota_percent are not here.
// Admission adds those, since it is the only layer holding both a capability
// snapshot and a quota snapshot.
// -------------------------------------------------------------------------------

package plugin

import (
	"slices"
	"strconv"
	"strings"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Prefix is reserved for attributes Vagabond publishes about a provider.
//
// Reserving it stops a plugin from shadowing an attribute the scheduler relies
// on, and makes a constraint on an unpublished name under this prefix a
// job-file error rather than a silent non-match.
const Prefix = "provider."

// The attributes derived from a capability snapshot.
//
// AttrArchitecture and AttrDrivers hold comma-separated sets, because a
// provider can offer several of each. They match by membership rather than
// equality, which is what SetValued reports; the operator that expresses this
// is settled when matching is implemented.
const (
	AttrArchitecture    = Prefix + "architecture"
	AttrDrivers         = Prefix + "drivers"
	AttrMaxDuration     = Prefix + "max_duration"
	AttrMaxCPU          = Prefix + "max_cpu"
	AttrMaxMemory       = Prefix + "max_memory"
	AttrInternetEgress  = Prefix + "internet"
	AttrPrivateNetwork  = Prefix + "private_network"
	AttrArbitraryImages = Prefix + "arbitrary_images"
	AttrEstimatedCost   = Prefix + "estimated_cost"
)

// AttrFreeQuotaPercent is how much of a provider's free-tier allowance remains.
//
// Declared here so that the attribute vocabulary is in one place, but no
// capability snapshot can populate it: the value comes from the quota ledger,
// and admission is the only layer holding both.
const AttrFreeQuotaPercent = Prefix + "free_quota_percent"

// MetaPrefix is where an operator's own tags on a provider live.
//
// Everything under it is open. Nomad separates fingerprinted node attributes
// from operator-set node metadata for the same reason, and cannot validate
// either because both are extensible. Ours splits differently: the attributes
// Vagabond derives are a closed set it can check, and this prefix is the escape
// hatch for everything an operator wants to say that Vagabond has no opinion
// about.
const MetaPrefix = Prefix + "meta."

var setValuedAttributes = []string{AttrArchitecture, AttrDrivers}

// knownAttributes is every name Vagabond publishes about a provider.
//
// A constraint naming something outside this set, and outside MetaPrefix, is a
// typo rather than a preference. Catching it matters because the alternative is
// a job that silently matches no provider and reports as having no capacity,
// which is the most misleading failure this can produce.
var knownAttributes = []string{
	AttrArchitecture,
	AttrDrivers,
	AttrMaxDuration,
	AttrMaxCPU,
	AttrMaxMemory,
	AttrInternetEgress,
	AttrPrivateNetwork,
	AttrArbitraryImages,
	AttrEstimatedCost,
	AttrFreeQuotaPercent,
}

// -------------------------------------------------------------------------
// PROJECTION
// -------------------------------------------------------------------------

// Attributes returns the dotted view of the snapshot.
//
// A limit the provider never advertised is left out of the map. Writing it as
// "0" would make a constraint read it as a real limit of zero. Cost is the
// exception and is published even at zero, because a provider that charges
// nothing is stating a fact rather than declining to state a limit.
func (c *Capabilities) Attributes() map[string]string {
	attrs := map[string]string{
		AttrInternetEgress:  strconv.FormatBool(c.InternetEgress),
		AttrPrivateNetwork:  strconv.FormatBool(c.PrivateNetwork),
		AttrArbitraryImages: strconv.FormatBool(c.ArbitraryImages),
		AttrEstimatedCost:   strconv.FormatInt(int64(c.EstimatedCost), 10),
	}

	if len(c.Architectures) > 0 {
		names := make([]string, 0, len(c.Architectures))
		for _, a := range c.Architectures {
			names = append(names, a.String())
		}

		attrs[AttrArchitecture] = strings.Join(names, ",")
	}

	if len(c.Drivers) > 0 {
		names := make([]string, 0, len(c.Drivers))
		for _, d := range c.Drivers {
			names = append(names, d.String())
		}

		attrs[AttrDrivers] = strings.Join(names, ",")
	}

	if c.MaxDuration > 0 {
		attrs[AttrMaxDuration] = c.MaxDuration.String()
	}

	if c.MaxResources.CPU > 0 {
		attrs[AttrMaxCPU] = strconv.Itoa(c.MaxResources.CPU)
	}

	if c.MaxResources.Memory > 0 {
		attrs[AttrMaxMemory] = strconv.Itoa(c.MaxResources.Memory)
	}

	return attrs
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// SetValued reports whether an attribute holds a comma-separated set, and so
// must be matched by membership rather than by equality.
func SetValued(name string) bool {
	return slices.Contains(setValuedAttributes, name)
}

// Reserved reports whether an attribute name belongs to Vagabond rather than to
// a provider plugin.
func Reserved(name string) bool {
	return strings.HasPrefix(name, Prefix)
}

// KnownAttributes returns every attribute Vagabond publishes about a provider.
//
// The returned slice is a copy, so a caller rendering it into an error cannot
// reorder the vocabulary for everyone else.
func KnownAttributes() []string {
	return slices.Clone(knownAttributes)
}

// Matchable reports whether a constraint may name this attribute.
//
// True for the attributes Vagabond publishes, and for anything under
// MetaPrefix, which is an operator's own and deliberately unchecked. A name
// that is neither is a mistake, not a preference nothing happens to satisfy.
func Matchable(name string) bool {
	if strings.HasPrefix(name, MetaPrefix) {
		return true
	}

	return slices.Contains(knownAttributes, name)
}
