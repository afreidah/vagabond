// -------------------------------------------------------------------------------
// Disabled Provider Contract
//
// Author: Alex Freidah
//
// The rules every store follows for providers turned off through the API. A
// second disable keeps the first record, enabling a provider deletes its
// record, and the list holds exactly the providers still disabled.
// -------------------------------------------------------------------------------

package statetest

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/state"
)

// -------------------------------------------------------------------------
// CONTRACT
// -------------------------------------------------------------------------

// ProviderStore is the part of a store that records disabled providers.
type ProviderStore interface {
	DisableProvider(ctx context.Context, name string, d state.Disabled) error
	EnableProvider(ctx context.Context, name string) error
	DisabledProviders(ctx context.Context) (map[string]state.Disabled, error)
}

// ProviderContract runs every disabled provider rule against a fresh store
// from newStore.
func ProviderContract(t *testing.T, newStore func(t *testing.T) ProviderStore) {
	check(t, providerRules, newStore)
}

// providerRules are the promises ProviderContract checks.
var providerRules = []rule[ProviderStore]{
	{"a second disable keeps the first time and name", firstDisableKept},
	{"enabling a provider deletes only its record", enableDeletes},
}

// -------------------------------------------------------------------------
// RULES
// -------------------------------------------------------------------------

// firstDisableKept: a provider disabled twice is listed once, with the time
// and name of the first disable.
func firstDisableKept(t *testing.T, s ProviderStore) {
	first := state.Disabled{At: time.Now().UTC().Truncate(time.Microsecond), By: "alice"}

	disable(t, s, "box", first)
	disable(t, s, "box", state.Disabled{At: first.At.Add(time.Minute), By: "bob"})

	if diff := cmp.Diff(map[string]state.Disabled{"box": first}, disabledIn(t, s)); diff != "" {
		t.Errorf("DisabledProviders() (-want +got):\n%s", diff)
	}
}

// enableDeletes: of two disabled providers, enabling one leaves only the
// other listed, and enabling a provider that was never disabled is no error.
func enableDeletes(t *testing.T, s ProviderStore) {
	d := state.Disabled{At: time.Now().UTC().Truncate(time.Microsecond)}

	disable(t, s, "a", d)
	disable(t, s, "b", d)

	for _, name := range []string{"a", "never-disabled"} {
		if err := s.EnableProvider(t.Context(), name); err != nil {
			t.Fatalf("EnableProvider(%s) = %v", name, err)
		}
	}

	if diff := cmp.Diff(map[string]state.Disabled{"b": d}, disabledIn(t, s)); diff != "" {
		t.Errorf("DisabledProviders() (-want +got):\n%s", diff)
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// disable disables name with d, failing the test if the store refuses.
func disable(t *testing.T, s ProviderStore, name string, d state.Disabled) {
	t.Helper()

	if err := s.DisableProvider(t.Context(), name, d); err != nil {
		t.Fatalf("DisableProvider(%s) = %v", name, err)
	}
}

// disabledIn lists the store's disabled providers, failing the test if it
// cannot.
func disabledIn(t *testing.T, s ProviderStore) map[string]state.Disabled {
	t.Helper()

	out, err := s.DisabledProviders(t.Context())
	if err != nil {
		t.Fatalf("DisabledProviders() = %v", err)
	}

	return out
}
