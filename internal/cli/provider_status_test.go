// -------------------------------------------------------------------------------
// Provider Status Tests
//
// Author: Alex Freidah
//
// The table against a server over fake providers, and its rendering directly.
// Usage from a real store is covered in the integration layer.
// -------------------------------------------------------------------------------

package cli

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/api"
)

// meteredConfig is a provider with two pools and one with none.
const meteredConfig = `
provider "metered" {
  type = "fake-container"

  pool "cpu" {
    meter  = "cpu_seconds"
    limit  = 192000
    period = "monthly"
  }

  pool "memory" {
    meter  = "gb_seconds"
    limit  = 360000
    period = "monthly"
  }
}

provider "open" { type = "fake-function" }
`

// Every provider is listed, its pools in their own units, a provider with none
// included, and the age of the usage stated.
func TestProviderStatus_ListsEveryProvider(t *testing.T) {
	s := serve(t, meteredConfig)

	code, stdout, stderr := s.at([]string{"provider", "status"})
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitSuccess, stderr)
	}

	for _, want := range []string{"metered", "cpu_seconds", "192000", "gb_seconds", "360000", "100%", "monthly", "open", "Usage refreshed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output does not mention %q:\n%s", want, stdout)
		}
	}
}

// A server that cannot be reached is a failure, not an empty table.
func TestProviderStatus_UnreachableServer(t *testing.T) {
	gone := httptest.NewServer(nil)
	gone.Close()

	code, _, _ := run("provider", "status", "-address", gone.URL)
	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}
}

// A provider's name appears once however many pools it has, and one with no
// pools fills every pool column with a dash.
func TestProviderRows(t *testing.T) {
	tests := []struct {
		name     string
		provider api.ProviderListStub
		want     string
	}{
		{
			name: "two pools",
			provider: api.ProviderListStub{Name: "gcp", Tier: 1, Enabled: true, Healthy: true, Pools: []api.ProviderPool{
				{Name: "cpu", Meter: "cpu_seconds", Period: "monthly", Used: 1.5, Limit: 100, FreePercent: 98},
				{Name: "memory", Meter: "gb_seconds", Period: "daily", Used: 3, Limit: 200, FreePercent: 98},
			}},
			want: "gcp\t1\ttrue\ttrue\tcpu\tcpu_seconds\t1.5\t100\t98%\tmonthly\n" +
				"\t\t\t\tmemory\tgb_seconds\t3\t200\t98%\tdaily\n",
		},
		{
			name:     "no pools",
			provider: api.ProviderListStub{Name: "home", Enabled: true},
			want:     "home\t0\ttrue\tfalse\t-\t-\t-\t-\t-\t-\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got strings.Builder

			providerRows(&got, &tt.provider)

			if got.String() != tt.want {
				t.Errorf("rows = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

// A figure keeps one decimal place, and a whole number stays whole.
func TestAmount(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{input: 0, want: "0"},
		{input: 0.04, want: "0"},
		{input: 1179, want: "1179"},
		{input: 1179.46, want: "1179.5"},
		{input: 192000, want: "192000"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := amount(tt.input); got != tt.want {
				t.Errorf("amount(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
