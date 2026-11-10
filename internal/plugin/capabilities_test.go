// -------------------------------------------------------------------------------
// Capability Model Tests
//
// Author: Alex Freidah
//
// The sharing tests carry the weight here. A capability snapshot is read by
// admission once per candidate from a single cached value, so a mutation that
// leaks between holders would be found late and be hard to attribute to the
// code that caused it.
// -------------------------------------------------------------------------------

package plugin

import (
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// CLONE
// -------------------------------------------------------------------------

// Assignment leaves both values on one backing array; this fails if Clone
// stops copying the slices.
func TestCapabilities_CloneDoesNotShareSlices(t *testing.T) {
	original := Capabilities{
		Drivers:       []job.DriverName{job.DriverContainer, job.DriverFunction},
		Architectures: []job.Arch{job.ArchAMD64},
	}

	clone := original.Clone()
	clone.Drivers[0] = job.DriverWorker
	clone.Architectures[0] = job.ArchARM64

	if original.Drivers[0] != job.DriverContainer {
		t.Errorf("mutating the clone changed the original driver to %q", original.Drivers[0])
	}

	if original.Architectures[0] != job.ArchAMD64 {
		t.Errorf("mutating the clone changed the original arch to %q", original.Architectures[0])
	}
}

// Appending to a clone must not reach into the original's spare capacity.
func TestCapabilities_CloneAppendIsIsolated(t *testing.T) {
	original := Capabilities{Drivers: make([]job.DriverName, 1, 4)}
	original.Drivers[0] = job.DriverContainer

	clone := original.Clone()
	clone.Drivers = append(clone.Drivers, job.DriverWorker)

	if len(original.Drivers) != 1 {
		t.Errorf("len(original.Drivers) = %d, want 1", len(original.Drivers))
	}
}

// Scalars carry over, and the pointer limits are copied rather than shared.
func TestCapabilities_CloneCopiesScalars(t *testing.T) {
	observed := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	original := Capabilities{
		MaxResources:     Limits{CPU: new(2000), Memory: new(4096)},
		MaxDuration:      15 * time.Minute,
		DefaultResources: Resources{CPU: 1000, Memory: 1024},
		InternetEgress:   true,
		ArbitraryImages:  true,
		ObservedAt:       observed,
	}

	clone := original.Clone()

	if clone.MaxDuration != original.MaxDuration || clone.DefaultResources != original.DefaultResources ||
		*clone.MaxResources.CPU != 2000 || *clone.MaxResources.Memory != 4096 {
		t.Error("clone lost a limit")
	}

	// The limits are pointers; a clone must not share them.
	*clone.MaxResources.CPU = 1
	if *original.MaxResources.CPU != 2000 {
		t.Error("changing the clone's CPU limit changed the original's")
	}

	if !clone.ObservedAt.Equal(observed) || clone.InternetEgress != true {
		t.Error("clone lost an observation or a flag")
	}
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// A cloud container platform runs any amd64 image with internet and no private
// network, within the limits given, observed now.
func TestCloudContainer(t *testing.T) {
	before := time.Now()
	caps := CloudContainer(8000, 32768, 24*time.Hour)

	if !caps.SupportsDriver(job.DriverContainer) || caps.SupportsDriver(job.DriverFunction) ||
		!caps.SupportsArch(job.ArchAMD64) || caps.SupportsArch(job.ArchARM64) {
		t.Errorf("drivers %v, architectures %v", caps.Drivers, caps.Architectures)
	}

	if *caps.MaxResources.CPU != 8000 || *caps.MaxResources.Memory != 32768 || caps.MaxDuration != 24*time.Hour {
		t.Errorf("limits = %d millicores, %d MiB, %s",
			*caps.MaxResources.CPU, *caps.MaxResources.Memory, caps.MaxDuration)
	}

	if !caps.InternetEgress || !caps.ArbitraryImages || caps.PrivateNetwork || caps.ObservedAt.Before(before) {
		t.Errorf("capabilities = %+v", caps)
	}
}

// Two snapshots share nothing, so a caller changing one cannot change the next.
func TestCloudContainer_SharesNothing(t *testing.T) {
	first := CloudContainer(8000, 32768, time.Hour)
	second := CloudContainer(8000, 32768, time.Hour)

	first.Drivers[0] = job.DriverWorker
	*first.MaxResources.CPU = 1

	if second.Drivers[0] != job.DriverContainer || *second.MaxResources.CPU != 8000 {
		t.Error("two snapshots share state")
	}
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// SupportsDriver answers from the advertised driver list.
func TestCapabilities_SupportsDriver(t *testing.T) {
	c := Capabilities{Drivers: []job.DriverName{job.DriverContainer}}

	if !c.SupportsDriver(job.DriverContainer) {
		t.Error("container driver reported unsupported")
	}

	if c.SupportsDriver(job.DriverFunction) {
		t.Error("function driver reported supported")
	}
}

// The zero value advertises nothing, which is the correct reading of a provider
// nothing is known about. Admission rejects what it cannot confirm.
func TestCapabilities_ZeroValueAdvertisesNothing(t *testing.T) {
	var c Capabilities

	if c.SupportsDriver(job.DriverContainer) {
		t.Error("zero value claims driver support")
	}

	if c.SupportsArch(job.ArchAMD64) {
		t.Error("zero value claims architecture support")
	}
}

// The limit is inclusive, and zero means no limit.
func TestCapabilities_WithinDuration(t *testing.T) {
	tests := []struct {
		name        string
		maxDuration time.Duration
		request     time.Duration
		want        bool
	}{
		{name: "under the limit", maxDuration: 15 * time.Minute, request: 5 * time.Minute, want: true},
		{name: "exactly at the limit", maxDuration: 15 * time.Minute, request: 15 * time.Minute, want: true},
		{name: "over the limit", maxDuration: 15 * time.Minute, request: 16 * time.Minute, want: false},
		{name: "no limit advertised", maxDuration: 0, request: 24 * time.Hour, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Capabilities{MaxDuration: tt.maxDuration}
			if got := c.WithinDuration(tt.request); got != tt.want {
				t.Errorf("WithinDuration(%v) = %v, want %v", tt.request, got, tt.want)
			}
		})
	}
}

// Each limit is checked against the size after defaults; nil admits anything.
func TestCapabilities_WithinResources(t *testing.T) {
	tests := []struct {
		name     string
		limits   Limits
		defaults Resources
		cpu      int
		memory   int
		want     bool
	}{
		{name: "both under", limits: Limits{CPU: new(2000), Memory: new(4096)}, cpu: 1000, memory: 2048, want: true},
		{name: "cpu over", limits: Limits{CPU: new(2000), Memory: new(4096)}, cpu: 4000, memory: 2048, want: false},
		{name: "memory over", limits: Limits{CPU: new(2000), Memory: new(4096)}, cpu: 1000, memory: 8192, want: false},
		{name: "no limits advertised", cpu: 99999, memory: 99999, want: true},
		{name: "zero room admits nothing", limits: Limits{CPU: new(0), Memory: new(0)}, cpu: 1, memory: 1, want: false},
		{
			name: "an undeclared size is checked at the default", limits: Limits{CPU: new(500)},
			defaults: Resources{CPU: 1000}, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Capabilities{MaxResources: tt.limits, DefaultResources: tt.defaults}
			if got := c.WithinResources(Resources{CPU: tt.cpu, Memory: tt.memory}); got != tt.want {
				t.Errorf("WithinResources(%d, %d) = %v, want %v", tt.cpu, tt.memory, got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// STALENESS
// -------------------------------------------------------------------------

// The bound is inclusive, and a snapshot never observed is always stale.
func TestCapabilities_StaleAt(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		observedAt time.Time
		maxAge     time.Duration
		want       bool
	}{
		{name: "fresh", observedAt: now.Add(-time.Minute), maxAge: 5 * time.Minute, want: false},
		{name: "exactly at the bound", observedAt: now.Add(-5 * time.Minute), maxAge: 5 * time.Minute, want: false},
		{name: "past the bound", observedAt: now.Add(-6 * time.Minute), maxAge: 5 * time.Minute, want: true},
		{name: "never observed", observedAt: time.Time{}, maxAge: time.Hour, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Capabilities{ObservedAt: tt.observedAt}
			if got := c.StaleAt(now, tt.maxAge); got != tt.want {
				t.Errorf("StaleAt() = %v, want %v", got, tt.want)
			}
		})
	}
}
