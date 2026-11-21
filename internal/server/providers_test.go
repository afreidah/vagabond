// -------------------------------------------------------------------------------
// Provider Route Tests
//
// Author: Alex Freidah
//
// Listing, disabling and enabling providers over the test fixtures, and the
// translation from admission's base units to what the wire carries.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
	"github.com/afreidah/vagabond/internal/state"
	"github.com/afreidah/vagabond/internal/state/memory"
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

// downProviders is a disabled provider store that cannot be reached.
type downProviders struct{}

// DisableProvider fails, as a write to an unreachable store does.
func (downProviders) DisableProvider(context.Context, string, state.Disabled) error {
	return state.ErrUnavailable
}

// EnableProvider fails, as a write to an unreachable store does.
func (downProviders) EnableProvider(context.Context, string) error {
	return state.ErrUnavailable
}

// DisabledProviders fails, as a read from an unreachable store does.
func (downProviders) DisabledProviders(context.Context) (map[string]state.Disabled, error) {
	return nil, state.ErrUnavailable
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

// -------------------------------------------------------------------------
// TURNING OFF AND ON
// -------------------------------------------------------------------------

// A disabled provider answers as disabled, with when, and a plan routed to it
// rejects it as provider-disabled. Enabling it answers as enabled again.
func TestProviders_DisableAndEnable(t *testing.T) {
	h := newHarness(t)

	var disabled api.ProviderListStub
	if status := h.call(t, http.MethodPost, "/v1/provider/box/disable", nil, &disabled); status != http.StatusOK {
		t.Fatalf("disable: status = %d, want 200", status)
	}

	// Plain HTTP has no client certificate to take a name from
	if disabled.Enabled || disabled.Disabled == nil || disabled.Disabled.At.IsZero() || disabled.Disabled.By != "" {
		t.Fatalf("disabled = %+v, want it disabled now by no one named", disabled)
	}

	var plan api.Plan
	h.call(t, http.MethodPost, "/v1/jobs/plan", api.PlanRequest{Source: sleepyJob}, &plan)

	rejected := plan.Tasks[0].Rejections
	if len(rejected) == 0 || rejected[0].Provider != "box" || rejected[0].Reason != string(scheduler.ReasonProviderDisabled) {
		t.Errorf("rejections = %+v, want box rejected as disabled", rejected)
	}

	var enabled api.ProviderListStub
	if status := h.call(t, http.MethodPost, "/v1/provider/box/enable", nil, &enabled); status != http.StatusOK ||
		!enabled.Enabled || enabled.Disabled != nil {
		t.Errorf("enable: status %d with %+v, want box enabled", status, enabled)
	}
}

// Disabling a provider already disabled keeps the time of the first disable.
func TestProviders_DisableTwiceKeepsTheFirst(t *testing.T) {
	h := newHarness(t)

	var first, second api.ProviderListStub

	h.call(t, http.MethodPost, "/v1/provider/box/disable", nil, &first)
	h.call(t, http.MethodPost, "/v1/provider/box/disable", nil, &second)

	if first.Disabled == nil || second.Disabled == nil || !second.Disabled.At.Equal(first.Disabled.At) {
		t.Errorf("disables = %+v then %+v, want the second to keep the first's time", first.Disabled, second.Disabled)
	}
}

// A name no provider has answers 404 on both routes, naming it.
func TestProviders_SwitchingAnUnknownProvider(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/v1/provider/nope/disable", "/v1/provider/nope/enable"} {
		var body api.Error
		if status := h.call(t, http.MethodPost, path, nil, &body); status != http.StatusNotFound ||
			!strings.Contains(body.Error, `"nope"`) {
			t.Errorf("POST %s = %d %q, want 404 naming nope", path, status, body.Error)
		}
	}
}

// A provider the configuration file turns off cannot be enabled through the
// API. The mock registry fails the test if the handler goes on to read the
// disabled set again.
func TestProviders_EnableRefusesWhatTheConfigurationTurnsOff(t *testing.T) {
	ctrl := gomock.NewController(t)

	reg := NewMockserverRegistry(ctrl)
	reg.EXPECT().Inputs(gomock.Any(), gomock.Any()).Return([]scheduler.Input{{Provider: "off"}})

	led, err := ledger.New(t.Context(), quota.Budgets{}, ledger.NewMemory(nil))
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}

	srv := httptest.NewServer(New(reg, led, memory.NewExecutions(), NewMockserverJobs(ctrl), memory.NewProviders(),
		slog.New(slog.DiscardHandler)).Handler())
	t.Cleanup(srv.Close)

	var body api.Error
	if status := (&harness{url: srv.URL}).call(t, http.MethodPost, "/v1/provider/off/enable", nil, &body); status !=
		http.StatusConflict || !strings.Contains(body.Error, "configuration file") {
		t.Errorf("enable = %d %q, want 409 pointing at the configuration file", status, body.Error)
	}
}

// With the store unreachable, disabling and enabling answer 503, and a failed
// read keeps the disabled set the registry last had.
func TestProviders_StoreUnreachable(t *testing.T) {
	reg, led := fixtures(t)
	s := New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)), downProviders{},
		slog.New(slog.DiscardHandler))

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	for _, path := range []string{"/v1/provider/box/disable", "/v1/provider/box/enable"} {
		if status := (&harness{url: srv.URL}).call(t, http.MethodPost, path, nil, nil); status != http.StatusServiceUnavailable {
			t.Errorf("POST %s = %d, want 503", path, status)
		}
	}

	reg.SetDisabled(map[string]state.Disabled{"box": {At: time.Now()}})

	if err := s.refreshDisabled(t.Context()); !errors.Is(err, state.ErrUnavailable) {
		t.Fatalf("refreshDisabled() = %v, want the store unreachable", err)
	}

	if in, _ := s.provider("box"); in.Disabled == nil {
		t.Error("box was enabled by a read that failed")
	}
}
