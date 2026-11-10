// -------------------------------------------------------------------------------
// Provider Status Tests
//
// Author: Alex Freidah
//
// GET /v1/providers over the test fixtures, and the translation from admission's
// base units to what the wire carries.
// -------------------------------------------------------------------------------

package server

import (
	"net/http"
	"testing"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// Base units per natural unit: a vCPU-second is a thousand millicores for a
// thousand milliseconds, a GB-second 1024 MiB for the same.
const (
	baseVCPUSecond = 1_000_000
	baseGBSecond   = 1_024_000
)

// meteredInput is a provider with one CPU and one memory pool, carrying usage.
func meteredInput(t *testing.T, usage quota.PoolUsage) *scheduler.Input {
	t.Helper()

	limits, err := quota.NewLimits([]quota.PoolSpec{
		{Name: "cpu", Meter: quota.MeterCPUSeconds, Limit: 1000, Period: quota.PeriodMonthly},
		{Name: "memory", Meter: quota.MeterGBSeconds, Limit: 2000, Period: quota.PeriodDaily},
	})
	if err != nil {
		t.Fatalf("NewLimits() = %v", err)
	}

	return &scheduler.Input{
		Provider: "gcp", Tier: 2, Enabled: true, Healthy: true, Limits: limits, Usage: usage,
	}
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// Every configured provider is listed with the time usage was read.
func TestProviders_ListsEveryProvider(t *testing.T) {
	h := newHarness(t)

	var list api.ProviderList

	if status := h.call(t, http.MethodGet, "/v1/providers", nil, &list); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	if len(list.Providers) != 2 {
		t.Fatalf("providers = %+v, want box and fn", list.Providers)
	}

	if list.Refreshed.IsZero() {
		t.Error("Refreshed is zero, though the ledger read usage when it was built")
	}
}

// Usage reaches the wire in each meter's natural unit, which is what an operator
// wrote the limit in.
func TestProviderStub_ConvertsToNaturalUnits(t *testing.T) {
	got := providerStub(meteredInput(t, quota.PoolUsage{"cpu": 250 * baseVCPUSecond, "memory": 500 * baseGBSecond}))

	if got.Name != "gcp" || got.Tier != 2 || !got.Enabled || !got.Healthy {
		t.Errorf("stub = %+v, want the provider's own standing", got)
	}

	want := []api.ProviderPool{
		{Name: "cpu", Meter: "cpu_seconds", Unit: "vCPU-seconds", Period: "monthly", Used: 250, Limit: 1000, FreePercent: 75},
		{Name: "memory", Meter: "gb_seconds", Unit: "GB-seconds", Period: "daily", Used: 500, Limit: 2000, FreePercent: 75},
	}

	if len(got.Pools) != len(want) {
		t.Fatalf("pools = %+v, want %+v", got.Pools, want)
	}

	for i := range want {
		if got.Pools[i] != want[i] {
			t.Errorf("pool %d = %+v, want %+v", i, got.Pools[i], want[i])
		}
	}
}

// A pool with nothing charged yet reads as untouched.
func TestProviderStub_UnusedPool(t *testing.T) {
	got := providerStub(meteredInput(t, nil))

	if pool := got.Pools[0]; pool.Used != 0 || pool.FreePercent != 100 {
		t.Errorf("pool = %+v, want nothing used and 100%% free", pool)
	}
}

// Reservations can take a pool past its limit, and what is left floors at zero
// rather than reading negative.
func TestProviderStub_PoolPastItsLimit(t *testing.T) {
	got := providerStub(meteredInput(t, quota.PoolUsage{"cpu": 1500 * baseVCPUSecond}))

	if pool := got.Pools[0]; pool.Used != 1500 || pool.FreePercent != 0 {
		t.Errorf("pool = %+v, want 1500 used and 0%% free", pool)
	}
}

// A provider with no budgets serializes an empty list, not null.
func TestProviderStub_NoPools(t *testing.T) {
	got := providerStub(&scheduler.Input{Provider: "home"})

	if got.Pools == nil || len(got.Pools) != 0 {
		t.Errorf("Pools = %#v, want empty and non-nil", got.Pools)
	}
}
