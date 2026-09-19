// -------------------------------------------------------------------------------
// Provider Construction
//
// Author: Alex Freidah
//
// Maps a configured type to the plugin that implements it. The one place in the
// tree that knows every provider by name, which is why it is here rather than
// in the scheduler: depguard forbids that package from importing a provider
// implementation at all, and this is the layer whose job it is to.
//
// Only the fakes exist so far. A real provider is added here when its plugin
// lands, and the shape of that change is one line.
// -------------------------------------------------------------------------------

package registry

import (
	"fmt"
	"slices"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// The provider types configuration can name.
//
// The fakes exist so that admission, scheduling, and a plan can be exercised
// with no cloud account configured anywhere. That is not only a testing
// convenience: if the test suite needed credentials, the plugin boundary would
// already have failed.
const (
	TypeFakeContainer = "fake-container"
	TypeFakeFunction  = "fake-function"
	TypeFakeWorker    = "fake-worker"
)

var providerTypes = []string{
	TypeFakeContainer,
	TypeFakeFunction,
	TypeFakeWorker,
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// Types returns every provider type configuration can name.
//
// The returned slice is a copy, so a caller rendering it into an error cannot
// reorder the vocabulary for everyone else.
func Types() []string {
	return slices.Clone(providerTypes)
}

// Build constructs the plugin for a configured type.
//
// The name is passed through rather than derived from the type, so that one
// deployment can register the same plugin twice against two accounts and a job
// can name them apart.
func Build(providerType, name string) (plugin.Provider, error) {
	switch providerType {
	case TypeFakeContainer:
		return plugin.NewFakeContainerProvider(name), nil

	case TypeFakeFunction:
		return plugin.NewFakeFunctionProvider(name), nil

	case TypeFakeWorker:
		return plugin.NewFakeWorkerProvider(name), nil

	default:
		return nil, fmt.Errorf("%w %q for provider %q: known types are %v",
			ErrUnknownProviderType, providerType, name, providerTypes)
	}
}
