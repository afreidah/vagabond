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
)

var setValuedAttributes = []string{AttrArchitecture, AttrDrivers}

// -------------------------------------------------------------------------
// PROJECTION
// -------------------------------------------------------------------------

// Attributes returns the dotted view of the snapshot.
//
// A limit the provider never advertised is left out of the map. Writing it as
// "0" would make a constraint read it as a real limit of zero.
func (c *Capabilities) Attributes() map[string]string {
	attrs := map[string]string{
		AttrInternetEgress:  strconv.FormatBool(c.InternetEgress),
		AttrPrivateNetwork:  strconv.FormatBool(c.PrivateNetwork),
		AttrArbitraryImages: strconv.FormatBool(c.ArbitraryImages),
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
