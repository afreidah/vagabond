// -------------------------------------------------------------------------------
// Attributes - the dotted view constraints and affinities match against
//
// Author: Alex Freidah
//
// Job files match on provider properties by dotted name, so Capabilities needs
// a string-keyed view. It is computed from the typed fields on every call
// rather than stored beside them, so the two cannot disagree. Quota-derived
// attributes are added by admission, the only layer holding both snapshots.
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

// Prefix is reserved for attributes Vagabond publishes about a provider, so a
// plugin cannot shadow one and a typo under it is a job-file error.
const Prefix = "provider."

// The attributes derived from a capability snapshot. AttrArchitecture and
// AttrDrivers hold comma-separated sets, which a job matches with set_contains.
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
// It comes from the quota ledger, so admission adds it, not the snapshot.
const AttrFreeQuotaPercent = Prefix + "free_quota_percent"

// AttrTier is the operator's preference for a provider, lower first. Like
// AttrFreeQuotaPercent it comes from configuration rather than the snapshot,
// and admission adds it.
const AttrTier = Prefix + "tier"

// MetaPrefix is where an operator's own tags on a provider live. Vagabond's
// attributes are a closed set it checks; everything under this is unchecked.
const MetaPrefix = Prefix + "meta."

// NodeLabelPrefix is where a pool node's labels live, from the agent's -label
// flags. Open like MetaPrefix, and only present when a pool's node is judged.
const NodeLabelPrefix = "node.label."

// setValuedAttributes are matched by membership rather than equality.
var setValuedAttributes = []string{AttrArchitecture, AttrDrivers}

// knownAttributes is every name Vagabond publishes about a provider. A
// constraint outside it and MetaPrefix is a typo, caught so the job does not
// silently match nothing and report as having no capacity.
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
	AttrTier,
}

// -------------------------------------------------------------------------
// PROJECTION
// -------------------------------------------------------------------------

// Attributes returns the dotted view of the snapshot. An unadvertised limit is
// left out rather than written as "0"; cost is published even at zero.
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

	if c.MaxResources.CPU != nil {
		attrs[AttrMaxCPU] = strconv.Itoa(*c.MaxResources.CPU)
	}

	if c.MaxResources.Memory != nil {
		attrs[AttrMaxMemory] = strconv.Itoa(*c.MaxResources.Memory)
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

// KnownAttributes returns a copy of every attribute Vagabond publishes about a
// provider, so a caller cannot reorder the vocabulary for everyone else.
func KnownAttributes() []string {
	return slices.Clone(knownAttributes)
}

// Matchable reports whether a constraint may name this attribute: one Vagabond
// publishes, or anything under MetaPrefix or NodeLabelPrefix, left unchecked.
func Matchable(name string) bool {
	if strings.HasPrefix(name, MetaPrefix) || strings.HasPrefix(name, NodeLabelPrefix) {
		return true
	}

	return slices.Contains(knownAttributes, name)
}
