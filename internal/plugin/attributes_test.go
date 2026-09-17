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
