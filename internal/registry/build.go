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
	"strings"

	"github.com/hashicorp/hcl/v2"

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

// Settings is everything a plugin constructor is handed.
//
// Config is the provider's own block, still undecoded. Passing the body rather
// than a decoded map is what lets a plugin report a typo against the line the
// operator wrote it on, and what keeps its field names out of internal/config.
//
// Credentials is already resolved to bytes, so a plugin never learns whether
// its secret came from a file, an environment variable or a command. Nil for
// providers that need none, which every fake does.
type Settings struct {
	Name        string
	Config      hcl.Body
	Credentials []byte
}

// Build constructs the plugin for a configured type.
//
// The name is passed through rather than derived from the type, so that one
// deployment can register the same plugin twice against two accounts and a job
// can name them apart.
//
// Diagnostics rather than an error, because a plugin decoding its own config
// reports against source ranges and a caller flattening that to a string would
// throw away the line number.
func Build(providerType string, settings Settings) (plugin.Provider, hcl.Diagnostics) {
	switch providerType {
	case TypeFakeContainer:
		return plugin.NewFakeContainerProvider(settings.Name), nil

	case TypeFakeFunction:
		return plugin.NewFakeFunctionProvider(settings.Name), nil

	case TypeFakeWorker:
		return plugin.NewFakeWorkerProvider(settings.Name), nil

	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unknown provider type",
			Detail: fmt.Sprintf("Provider %q is of type %q, which nothing "+
				"implements. Known types are %s.",
				settings.Name, providerType, strings.Join(providerTypes, ", ")),
		}}
	}
}
