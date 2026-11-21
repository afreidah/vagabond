//go:build integration

// -------------------------------------------------------------------------------
// Disabling Providers Across Servers
//
// Author: Alex Freidah
//
// Servers on one store, each with its own registry built from the same
// configuration, as separate processes would have. A change made through one
// server reaches the others only through the store.
// -------------------------------------------------------------------------------

package integration

import (
	"context"
	"testing"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/scheduler"
	"github.com/afreidah/vagabond/internal/server"
	"github.com/afreidah/vagabond/internal/testkit"
)

// A provider disabled through one server is rejected by a second once it
// reads the store again, and by a server started afterwards. Enabling it
// through the second lets the first accept it again.
func TestDisable_ReachesEveryServerOnTheStore(t *testing.T) {
	first := newHarness(t, twoProviders)
	second := &harness{t: t, registry: providers(t, twoProviders), store: first.store}

	a := first.serve(server.WithOwner("server-a"))
	b := second.serve(server.WithOwner("server-b"))

	if _, err := a.DisableProvider(context.Background(), "a"); err != nil {
		t.Fatalf("DisableProvider() = %v", err)
	}

	testkit.Eventually(t, runTimeout, "the second server to reject a", func() bool {
		return rejectedAsDisabled(t, b, "a")
	})

	// A server reads the disabled providers before it serves its first request
	late := &harness{t: t, registry: providers(t, twoProviders), store: first.store}
	if c := late.serve(server.WithOwner("server-c")); !rejectedAsDisabled(t, c, "a") {
		t.Error("a server started after the disable accepted a")
	}

	if _, err := b.EnableProvider(context.Background(), "a"); err != nil {
		t.Fatalf("EnableProvider() = %v", err)
	}

	testkit.Eventually(t, runTimeout, "the first server to accept a again", func() bool {
		return !rejectedAsDisabled(t, a, "a")
	})
}

// rejectedAsDisabled plans a job either provider can run through client, and
// reports whether provider is rejected as disabled.
func rejectedAsDisabled(t *testing.T, client *api.Client, provider string) bool {
	t.Helper()

	plan, err := client.Plan(context.Background(), "", api.PlanRequest{Source: containerJob("1m")})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	for _, r := range plan.Tasks[0].Rejections {
		if r.Provider == provider && r.Reason == string(scheduler.ReasonProviderDisabled) {
			return true
		}
	}

	return false
}
