// -------------------------------------------------------------------------------
// Provider Disable and Enable Tests
//
// Author: Alex Freidah
//
// Both commands against a server with every store in memory. Other servers
// seeing the change through a shared store is covered in the integration
// layer.
// -------------------------------------------------------------------------------

package cli

import (
	"strings"
	"testing"
)

// Disabling a provider says so, provider status then shows when, and enabling
// it says it is healthy again.
func TestProviderSwitch_DisableThenEnable(t *testing.T) {
	s := serve(t, meteredConfig)

	code, stdout, stderr := s.at([]string{"provider", "disable"}, "open")
	if code != ExitSuccess || !strings.Contains(stdout, `Provider "open" is now disabled.`) {
		t.Fatalf("provider disable: exit %d\n%s%s", code, stdout, stderr)
	}

	code, stdout, stderr = s.at([]string{"provider", "status"}, "open")
	if code != ExitSuccess || !strings.Contains(stdout, "Disabled = at ") {
		t.Errorf("provider status: exit %d, want when it was disabled\n%s%s", code, stdout, stderr)
	}

	code, stdout, stderr = s.at([]string{"provider", "enable"}, "open")
	if code != ExitSuccess || !strings.Contains(stdout, `Provider "open" is now healthy.`) {
		t.Errorf("provider enable: exit %d\n%s%s", code, stdout, stderr)
	}
}

// Enabling a provider the configuration file turns off fails with the
// server's reason.
func TestProviderSwitch_EnableRefusesTheConfiguration(t *testing.T) {
	s := serve(t, `
provider "off" {
  type    = "fake-container"
  enabled = false
}
`)

	code, _, stderr := s.at([]string{"provider", "enable"}, "off")
	if code != ExitFailure || !strings.Contains(stderr, "configuration file") {
		t.Errorf("provider enable: exit %d, want the configuration named\n%s", code, stderr)
	}
}

// Each of the two commands has its own help and synopsis.
func TestProviderSwitch_EachHasItsOwnHelp(t *testing.T) {
	disable, enable := &ProviderSwitchCommand{}, &ProviderSwitchCommand{enable: true}

	if !strings.HasPrefix(disable.Help(), "Usage: vagabond provider disable") ||
		!strings.HasPrefix(enable.Help(), "Usage: vagabond provider enable") {
		t.Errorf("help = %q and %q, want each command's own", disable.Help(), enable.Help())
	}

	if disable.Synopsis() == enable.Synopsis() {
		t.Errorf("both synopses are %q", disable.Synopsis())
	}
}
