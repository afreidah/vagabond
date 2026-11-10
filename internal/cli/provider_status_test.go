// -------------------------------------------------------------------------------
// Provider Status Tests
//
// Author: Alex Freidah
//
// The list and the detail view against a server over fake providers, and their
// rendering directly. Usage from a real store is covered in the integration
// layer.
// -------------------------------------------------------------------------------

package cli

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/api"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

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

// -------------------------------------------------------------------------
// COMMAND
// -------------------------------------------------------------------------

// Every provider gets exactly one row, its pools summarized on it, a provider
// with none said so, and the age of the usage stated.
func TestProviderStatus_ListsEveryProvider(t *testing.T) {
	s := serve(t, meteredConfig)

	code, stdout, stderr := s.at([]string{"provider", "status"})
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitSuccess, stderr)
	}

	table, _, _ := strings.Cut(stdout, "\n\n")
	lines := strings.Split(table, "\n")

	if len(lines) != 3 {
		t.Fatalf("table has %d lines, want a header and one per provider:\n%s", len(lines), stdout)
	}

	if !strings.HasPrefix(lines[1], "metered") || !strings.Contains(lines[1], "cpu 100%, memory 100%") {
		t.Errorf("metered row = %q", lines[1])
	}

	if !strings.HasPrefix(lines[2], "open") || !strings.Contains(lines[2], "no quota") {
		t.Errorf("open row = %q", lines[2])
	}

	if !strings.Contains(stdout, "Usage as of") {
		t.Errorf("output does not state the usage's age:\n%s", stdout)
	}
}

// A name shows that provider's pools in full, in their own units.
func TestProviderStatus_OneProvider(t *testing.T) {
	s := serve(t, meteredConfig)

	code, stdout, stderr := s.at([]string{"provider", "status"}, "metered")
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d\n%s", code, ExitSuccess, stderr)
	}

	for _, want := range []string{"Name   = metered", "Tier   = 0", "192,000", "360,000", "vCPU-seconds", "GB-seconds", "100%", "monthly", "Usage as of"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output does not mention %q:\n%s", want, stdout)
		}
	}

	if strings.Contains(stdout, "open") {
		t.Errorf("the detail view lists another provider:\n%s", stdout)
	}
}

// A provider with no pools says so rather than showing an empty table.
func TestProviderStatus_OneProviderWithoutPools(t *testing.T) {
	s := serve(t, meteredConfig)

	code, stdout, _ := s.at([]string{"provider", "status"}, "open")
	if code != ExitSuccess || !strings.Contains(stdout, "No quota pools.") {
		t.Fatalf("exit code = %d, output:\n%s", code, stdout)
	}
}

// A name nothing is configured as fails, listing the names that are.
func TestProviderStatus_UnknownProvider(t *testing.T) {
	s := serve(t, meteredConfig)

	code, _, stderr := s.at([]string{"provider", "status"}, "nope")
	if code != ExitFailure {
		t.Fatalf("exit code = %d, want %d", code, ExitFailure)
	}

	if !strings.Contains(stderr, `no provider is named "nope"`) || !strings.Contains(stderr, "metered, open") {
		t.Errorf("stderr = %q", stderr)
	}
}

// More than one name is a usage error.
func TestProviderStatus_TooManyArguments(t *testing.T) {
	s := serve(t, meteredConfig)

	if code, _, _ := s.at([]string{"provider", "status"}, "metered", "open"); code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
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

// -------------------------------------------------------------------------
// LIST
// -------------------------------------------------------------------------

// Rows go by tier and then name, each provider's state in one word.
func TestProviderTable(t *testing.T) {
	pools := []api.ProviderPool{{Name: "cpu", FreePercent: 95}}

	got := providerTable([]api.ProviderListStub{
		{Name: "homelab", Tier: 1, Enabled: true, Healthy: true},
		{Name: "ibm", Tier: 0, Enabled: true, Healthy: true, Pools: pools},
		{Name: "gcp", Tier: 0, Enabled: true, Healthy: true, Pools: pools},
		{Name: "off", Tier: 0},
		{Name: "sick", Tier: 2, Enabled: true},
	})

	want := []struct{ name, state string }{
		{"gcp", "healthy"}, {"ibm", "healthy"}, {"off", "disabled"}, {"homelab", "healthy"}, {"sick", "unhealthy"},
	}

	lines := strings.Split(got, "\n")[1:]
	if len(lines) != len(want) {
		t.Fatalf("rows:\n%s", got)
	}

	for i, w := range want {
		fields := strings.Fields(lines[i])
		if fields[0] != w.name || fields[2] != w.state {
			t.Errorf("row %d = %q, want %s %s", i, lines[i], w.name, w.state)
		}
	}
}

// Each pool is named with its share left; no pools reads as no quota.
func TestQuotaLeft(t *testing.T) {
	two := []api.ProviderPool{{Name: "cpu", FreePercent: 95}, {Name: "memory", FreePercent: 3}}

	if got := quotaLeft(two); got != "cpu 95%, memory 3%" {
		t.Errorf("quotaLeft(two) = %q", got)
	}

	if got := quotaLeft(nil); got != "no quota" {
		t.Errorf("quotaLeft(nil) = %q", got)
	}
}

// -------------------------------------------------------------------------
// DETAIL
// -------------------------------------------------------------------------

// The detail view gives the provider's fields, then one row per pool with
// whole figures, its unit and its period.
func TestProviderDetail(t *testing.T) {
	got := providerDetail(&api.ProviderListStub{
		Name: "gcp", Tier: 0, Enabled: true, Healthy: true,
		Pools: []api.ProviderPool{
			{Name: "cpu", Unit: "vCPU-seconds", Period: "monthly", Used: 9430.5, Limit: 192000, FreePercent: 95},
			{Name: "memory", Unit: "GB-seconds", Period: "daily", Used: 18843.3, Limit: 360000, FreePercent: 94},
		},
	})

	lines := strings.Split(got, "\n")

	if lines[0] != "Name   = gcp" || lines[1] != "Tier   = 0" || lines[2] != "Status = healthy" || lines[3] != "" {
		t.Errorf("fields:\n%s", got)
	}

	for i, want := range [][]string{
		{"POOL", "USED", "LIMIT", "UNIT", "LEFT", "PERIOD"},
		{"cpu", "9,431", "192,000", "vCPU-seconds", "95%", "monthly"},
		{"memory", "18,843", "360,000", "GB-seconds", "94%", "daily"},
	} {
		if fields := strings.Fields(lines[4+i]); strings.Join(fields, " ") != strings.Join(want, " ") {
			t.Errorf("row %d = %q, want %v", i, lines[4+i], want)
		}
	}
}

// A provider with no pools says so.
func TestProviderDetail_NoPools(t *testing.T) {
	got := providerDetail(&api.ProviderListStub{Name: "homelab", Tier: 1, Enabled: true, Healthy: true})

	if !strings.HasSuffix(got, "\n\nNo quota pools.") {
		t.Errorf("detail = %q", got)
	}
}

// Only the named provider is found, and nothing when no name matches.
func TestFindProvider(t *testing.T) {
	providers := []api.ProviderListStub{{Name: "gcp"}, {Name: "ibm"}}

	if got := findProvider(providers, "ibm"); got == nil || got.Name != "ibm" {
		t.Errorf("findProvider(ibm) = %+v", got)
	}

	if got := findProvider(providers, "aws"); got != nil {
		t.Errorf("findProvider(aws) = %+v, want nil", got)
	}
}

// -------------------------------------------------------------------------
// FORMATTING
// -------------------------------------------------------------------------

// A figure is rounded to a whole number and grouped in thousands.
func TestWhole(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{input: 0, want: "0"},
		{input: 0.4, want: "0"},
		{input: 999, want: "999"},
		{input: 1000, want: "1,000"},
		{input: 9430.5, want: "9,431"},
		{input: 192000, want: "192,000"},
		{input: 1234567, want: "1,234,567"},
		{input: -1500, want: "-1,500"},
	}

	for _, tt := range tests {
		if got := whole(tt.input); got != tt.want {
			t.Errorf("whole(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// The footer gives the snapshot's time and age, never a negative one, and says
// when usage has never been read.
func TestUsageAge(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 43, 21, 0, time.UTC)
	read := time.Date(2026, 11, 10, 9, 43, 17, 0, time.UTC)

	if got := usageAge(read, now); got != "Usage as of 2026-11-10 09:43:17Z (4s ago)." {
		t.Errorf("usageAge() = %q", got)
	}

	if got := usageAge(now, read); got != "Usage as of 2026-11-10 09:43:21Z (0s ago)." {
		t.Errorf("usageAge(future) = %q", got)
	}

	if got := usageAge(time.Time{}, now); got != "Usage has not been read yet." {
		t.Errorf("usageAge(zero) = %q", got)
	}
}
