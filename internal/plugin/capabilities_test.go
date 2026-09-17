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

// A struct assignment copies slice headers and leaves both values pointing at
// one backing array. Clone exists to break that sharing, and this is the test
// that fails if the slices.Clone calls are ever dropped.
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

func TestCapabilities_CloneCopiesScalars(t *testing.T) {
	observed := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	original := Capabilities{
		MaxResources:    Resources{CPU: 2000, Memory: 4096},
		MaxDuration:     15 * time.Minute,
		InternetEgress:  true,
		ArbitraryImages: true,
		ObservedAt:      observed,
	}

	clone := original.Clone()

	if clone.MaxDuration != original.MaxDuration || clone.MaxResources != original.MaxResources {
		t.Error("clone lost a limit")
	}

	if !clone.ObservedAt.Equal(observed) || clone.InternetEgress != true {
		t.Error("clone lost an observation or a flag")
	}
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

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

func TestCapabilities_WithinResources(t *testing.T) {
	tests := []struct {
		name      string
		maxCPU    int
		maxMemory int
		cpu       int
		memory    int
		want      bool
	}{
		{name: "both under", maxCPU: 2000, maxMemory: 4096, cpu: 1000, memory: 2048, want: true},
		{name: "cpu over", maxCPU: 2000, maxMemory: 4096, cpu: 4000, memory: 2048, want: false},
		{name: "memory over", maxCPU: 2000, maxMemory: 4096, cpu: 1000, memory: 8192, want: false},
		{name: "no limits advertised", cpu: 99999, memory: 99999, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Capabilities{MaxResources: Resources{CPU: tt.maxCPU, Memory: tt.maxMemory}}
			if got := c.WithinResources(Resources{CPU: tt.cpu, Memory: tt.memory}); got != tt.want {
				t.Errorf("WithinResources(%d, %d) = %v, want %v", tt.cpu, tt.memory, got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// STALENESS
// -------------------------------------------------------------------------

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
