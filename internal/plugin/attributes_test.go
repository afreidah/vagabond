// -------------------------------------------------------------------------------
// Attribute Projection Tests
//
// Author: Alex Freidah
//
// The projection is derived rather than stored, so the tests that matter are
// the ones pinning what a job file can rely on seeing: which names appear, and
// what an unadvertised limit looks like.
// -------------------------------------------------------------------------------

package plugin

import (
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// PROJECTION
// -------------------------------------------------------------------------

func TestCapabilities_Attributes(t *testing.T) {
	c := FixtureFunction(time.Now())
	attrs := c.Attributes()

	want := map[string]string{
		AttrDrivers:         "function",
		AttrArchitecture:    "amd64,arm64",
		AttrMaxDuration:     "15m0s",
		AttrMaxCPU:          "1800",
		AttrMaxMemory:       "10240",
		AttrInternetEgress:  "true",
		AttrArbitraryImages: "false",
		AttrPrivateNetwork:  "false",
	}

	for name, wantValue := range want {
		if got := attrs[name]; got != wantValue {
			t.Errorf("attrs[%q] = %q, want %q", name, got, wantValue)
		}
	}
}

// An unadvertised limit is absent, not "0". A constraint comparing against a
// zero here would read "no limit" as "a limit of nothing".
func TestCapabilities_AttributesOmitUnadvertisedLimits(t *testing.T) {
	c := FixtureContainer(time.Now())
	attrs := c.Attributes()

	if _, ok := attrs[AttrMaxDuration]; ok {
		t.Errorf("%s present for a provider advertising no duration limit", AttrMaxDuration)
	}

	if _, ok := attrs[AttrMaxCPU]; !ok {
		t.Errorf("%s missing for a provider that does advertise one", AttrMaxCPU)
	}
}

// The worker fixture offers no architecture at all, which must read as the
// attribute being absent rather than as an empty string that could match.
func TestCapabilities_AttributesOmitEmptySets(t *testing.T) {
	c := FixtureWorker(time.Now())
	attrs := c.Attributes()

	if got, ok := attrs[AttrArchitecture]; ok {
		t.Errorf("%s = %q for a provider offering none, want absent", AttrArchitecture, got)
	}
}

// Booleans are always present, because "this provider does not do that" is a
// claim a constraint needs to be able to match on.
func TestCapabilities_AttributesAlwaysIncludeBooleans(t *testing.T) {
	var c Capabilities
	attrs := c.Attributes()

	for _, name := range []string{AttrInternetEgress, AttrPrivateNetwork, AttrArbitraryImages} {
		if got, ok := attrs[name]; !ok || got != "false" {
			t.Errorf("attrs[%q] = %q (present=%v), want \"false\"", name, got, ok)
		}
	}
}

func TestCapabilities_AttributesAreDerived(t *testing.T) {
	c := Capabilities{Drivers: []job.DriverName{job.DriverContainer}}

	first := c.Attributes()
	first[AttrDrivers] = "mutated"

	if c.Attributes()[AttrDrivers] != "container" {
		t.Error("mutating a returned map changed what the next call produces")
	}
}

// -------------------------------------------------------------------------
// ATTRIBUTE QUERIES
// -------------------------------------------------------------------------

func TestSetValued(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: AttrArchitecture, want: true},
		{name: AttrDrivers, want: true},
		{name: AttrMaxDuration, want: false},
		{name: AttrInternetEgress, want: false},
		{name: "provider.unknown", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SetValued(tt.name); got != tt.want {
				t.Errorf("SetValued(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// Every set-valued attribute must actually render a set, or membership matching
// would be defined over something that is not one.
func TestSetValuedAttributesRenderCommaSeparated(t *testing.T) {
	c := FixtureFunction(time.Now())
	attrs := c.Attributes()

	value, ok := attrs[AttrArchitecture]
	if !ok {
		t.Fatalf("%s missing from the projection", AttrArchitecture)
	}

	if !strings.Contains(value, ",") {
		t.Errorf("%s = %q, want a comma-separated set", AttrArchitecture, value)
	}
}

func TestReserved(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: AttrArchitecture, want: true},
		{name: "provider.anything", want: true},
		{name: "node.class", want: false},
		{name: "", want: false},
		{name: "providers.architecture", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Reserved(tt.name); got != tt.want {
				t.Errorf("Reserved(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// The fixtures encode the distinctions Chunk 3 depends on. If these drift, the
// admission tests stop testing what they claim to.
func TestFixtures_EncodeTheFamilyDistinctions(t *testing.T) {
	now := time.Now()

	if !FixtureContainer(now).ArbitraryImages {
		t.Error("the container fixture must run arbitrary images")
	}

	if FixtureFunction(now).ArbitraryImages {
		t.Error("the function fixture must not claim arbitrary images")
	}

	if FixtureFunction(now).MaxDuration != 15*time.Minute {
		t.Error("the function fixture must carry Lambda's 15 minute limit")
	}

	if FixtureContainer(now).MaxDuration != 0 {
		t.Error("the container fixture must advertise no duration limit")
	}

	if len(FixtureWorker(now).Architectures) != 0 {
		t.Error("the worker fixture must offer no architecture")
	}
}

// -------------------------------------------------------------------------
// THE ATTRIBUTE VOCABULARY
// -------------------------------------------------------------------------

// Every attribute the projection can emit has to be in the known set, or a
// constraint naming one would be reported as a typo.
func TestKnownAttributes_CoversTheProjection(t *testing.T) {
	c := FixtureFunction(time.Now())

	known := make(map[string]bool, len(KnownAttributes()))
	for _, name := range KnownAttributes() {
		known[name] = true
	}

	for name := range c.Attributes() {
		if !known[name] {
			t.Errorf("the projection emits %q, which is not in the known set", name)
		}
	}
}

// The quota attribute is emitted by admission rather than by a capability
// snapshot, so nothing else would catch it going missing.
func TestKnownAttributes_IncludesTheQuotaAttribute(t *testing.T) {
	if !Matchable(AttrFreeQuotaPercent) {
		t.Errorf("%q is not matchable", AttrFreeQuotaPercent)
	}
}

func TestKnownAttributes_ReturnsCopy(t *testing.T) {
	first := KnownAttributes()
	first[0] = "mutated"

	if KnownAttributes()[0] == "mutated" {
		t.Error("KnownAttributes() exposed the package-level vocabulary to mutation")
	}
}

// The closed set is checkable; the operator's own prefix is not. That split is
// what lets a typo be caught without forbidding an operator from tagging a
// provider with whatever they like.
func TestMatchable(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: AttrArchitecture, want: true},
		{name: AttrFreeQuotaPercent, want: true},
		{name: MetaPrefix + "region", want: true},
		{name: MetaPrefix + "anything at all", want: true},
		{name: "provider.architekture", want: false},
		{name: "provider.meta", want: false},
		{name: "node.class", want: false},
		{name: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Matchable(tt.name); got != tt.want {
				t.Errorf("Matchable(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
