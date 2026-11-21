// -------------------------------------------------------------------------------
// Memory Disabled Provider Tests
//
// Author: Alex Freidah
//
// The memory store follows the same disabled provider rules as Postgres, and
// hands out copies, so a caller cannot change what it holds.
// -------------------------------------------------------------------------------

package memory

import (
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/state"
	"github.com/afreidah/vagabond/internal/state/statetest"
)

// The memory store keeps the disabled provider rules every store keeps.
func TestProviders_Contract(t *testing.T) {
	statetest.ProviderContract(t, func(*testing.T) statetest.ProviderStore { return NewProviders() })
}

// Deleting from the map DisabledProviders returned leaves the provider
// disabled in the store.
func TestProviders_ListIsACopy(t *testing.T) {
	s := NewProviders()

	if err := s.DisableProvider(t.Context(), "box", state.Disabled{At: time.Now()}); err != nil {
		t.Fatalf("DisableProvider() = %v", err)
	}

	listed, _ := s.DisabledProviders(t.Context())
	delete(listed, "box")

	if again, _ := s.DisabledProviders(t.Context()); len(again) != 1 {
		t.Errorf("DisabledProviders() = %v after changing a copy, want box still disabled", again)
	}
}
