// -------------------------------------------------------------------------------
// Live OCI Functions Test
//
// Author: Alex Freidah
//
// Invokes a function deployed to a real tenancy, the Python one on the OCI
// Functions provider page, and reads back its reply and a timeout. Skipped
// unless a function is named, so the default suite needs no cloud account
// anywhere:
//
//	VAGABOND_OCI_FUNCTION=<function OCID> VAGABOND_OCI_REGION=us-ashburn-1 \
//	VAGABOND_OCI_TENANCY=<tenancy OCID> VAGABOND_OCI_USER=<user OCID> \
//	VAGABOND_OCI_FINGERPRINT=<key fingerprint> VAGABOND_OCI_KEY_FILE=<PEM path> \
//	go test -run TestLive -v -timeout 10m ./internal/providers/oci/
//
// This is the only test that catches a field or an error code Oracle renamed.
// -------------------------------------------------------------------------------

package oci

import (
	"os"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/plugin/plugintest"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// SETUP
// -------------------------------------------------------------------------

// liveProvider builds a provider against a real tenancy and returns it with
// the function to invoke, or skips.
func liveProvider(t *testing.T) (*Provider, string) {
	t.Helper()

	function := os.Getenv("VAGABOND_OCI_FUNCTION")
	if function == "" {
		t.Skip("VAGABOND_OCI_FUNCTION is unset")
	}

	key, err := os.ReadFile(os.Getenv("VAGABOND_OCI_KEY_FILE")) //nolint:gosec // the operator names the key to test with
	if err != nil {
		t.Fatalf("reading VAGABOND_OCI_KEY_FILE: %v", err)
	}

	p, diags := New(t.Context(), "oci-live", plugintest.Body(t, `
region      = "`+os.Getenv("VAGABOND_OCI_REGION")+`"
tenancy     = "`+os.Getenv("VAGABOND_OCI_TENANCY")+`"
user        = "`+os.Getenv("VAGABOND_OCI_USER")+`"
fingerprint = "`+os.Getenv("VAGABOND_OCI_FINGERPRINT")+`"
`), key)
	if diags.HasErrors() {
		t.Fatalf("building the provider: %s", diags.Error())
	}

	return p, function
}

// invokeLive submits a task invoking function with args, the env carrying a
// $ the reply must echo as written.
func invokeLive(t *testing.T, p *Provider, id execution.ID, function, args string) plugin.Submission {
	t.Helper()

	task := plugintest.FunctionTask(t, "function = \""+function+"\"\nargs = "+args, `VAGABOND_LIVE = "$BUILD_ID"`)

	sub, err := p.Submit(t.Context(), id, task)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	t.Logf("request %s: state=%s exit=%d billed=%+v logs=%q",
		sub.ProviderID, sub.State, *sub.Result.ExitCode, sub.Result.Billed, sub.Result.Logs)

	return sub
}

// -------------------------------------------------------------------------
// ROUND TRIPS
// -------------------------------------------------------------------------

// The function's reply comes back as the task's exit code and output, with
// the event's execution, args and env as sent, billed at its memory.
func TestLiveRoundTrip(t *testing.T) {
	p, function := liveProvider(t)
	id := testkit.NewID(t)

	sub := invokeLive(t, p, id, function, `["a b", "$HOME"]`)

	if sub.State != execution.StateFailed || *sub.Result.ExitCode != 3 {
		t.Errorf("state = %s, exit = %d; want failed with exit 3", sub.State, *sub.Result.ExitCode)
	}

	if want := "hello " + id.String() + " [a b|$HOME] $BUILD_ID\n"; string(sub.Result.Logs) != want {
		t.Errorf("logs = %q, want %q", sub.Result.Logs, want)
	}

	if sub.ProviderID == function || sub.Result.Billed == nil || sub.Result.Billed.Memory != 128 {
		t.Errorf("provider id = %q, billed = %+v; want a request ID and the function's 128 MB", sub.ProviderID, sub.Result.Billed)
	}
}

// A function running past its timeout fails the task with OCI's error as its
// output, rather than failing the call.
func TestLiveTimeoutIsAFailedTask(t *testing.T) {
	p, function := liveProvider(t)

	sub := invokeLive(t, p, testkit.NewID(t), function, `["sleep"]`)

	if sub.State != execution.StateFailed || *sub.Result.ExitCode != 1 || !strings.Contains(string(sub.Result.Logs), "FunctionInvokeTimeout") {
		t.Errorf("state = %s, exit = %d, logs = %q; want exit 1 with FunctionInvokeTimeout",
			sub.State, *sub.Result.ExitCode, sub.Result.Logs)
	}
}
