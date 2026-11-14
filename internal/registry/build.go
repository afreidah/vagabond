// -------------------------------------------------------------------------------
// Provider Construction
//
// Author: Alex Freidah
//
// Maps a configured type to the plugin that implements it. This is the one
// place in the tree that knows every provider by name; depguard forbids the
// scheduler from importing a provider implementation. A new provider type is
// one case in Build.
// -------------------------------------------------------------------------------

package registry

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/providers/aws"
	"github.com/afreidah/vagabond/internal/providers/gcp"
	"github.com/afreidah/vagabond/internal/providers/ibm"
	"github.com/afreidah/vagabond/internal/providers/pool"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// The fake provider types, which let admission, scheduling and a plan run with
// no cloud account configured.
const (
	TypeFakeContainer = "fake-container"
	TypeFakeFunction  = "fake-function"
	TypeFakeWorker    = "fake-worker"
)

// providerTypes is every type Build constructs, in the order errors list them.
var providerTypes = []string{
	gcp.Type,
	ibm.Type,
	aws.Type,
	pool.Type,

	TypeFakeContainer,
	TypeFakeFunction,
	TypeFakeWorker,
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// Types returns every provider type configuration can name, as a copy a
// caller may reorder.
func Types() []string {
	return slices.Clone(providerTypes)
}

// Settings is everything a plugin constructor is handed.
//
// Config is the provider's block still undecoded, so a plugin can report a
// typo against its line and its field names stay out of internal/config.
// Credentials is already resolved to bytes, nil for a provider that needs
// none. Nodes is the server's connected agent nodes, which a containerd
// provider is made of; nil outside a server.
type Settings struct {
	Name        string
	Config      hcl.Body
	Credentials []byte
	Nodes       *nodes.Conns
}

// Build constructs the plugin for a configured type under the configured name,
// so one type can be registered twice against two accounts. It returns
// diagnostics rather than an error so a config mistake keeps its source range.
func Build(
	ctx context.Context, providerType string, settings Settings,
) (plugin.Provider, hcl.Diagnostics) {
	switch providerType {
	case gcp.Type:
		// A failed build returns a nil interface, not one holding a nil pointer.
		p, diags := gcp.New(ctx, settings.Name, settings.Config, settings.Credentials)
		if diags.HasErrors() {
			return nil, diags
		}

		return p, diags

	case ibm.Type:
		p, diags := ibm.New(ctx, settings.Name, settings.Config, settings.Credentials)
		if diags.HasErrors() {
			return nil, diags
		}

		return p, diags

	case aws.Type:
		p, diags := aws.New(ctx, settings.Name, settings.Config, settings.Credentials)
		if diags.HasErrors() {
			return nil, diags
		}

		return p, diags

	case pool.Type:
		// Made of a server's connected nodes, so only a server has one.
		if settings.Nodes == nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Agent nodes without a server",
				Detail: fmt.Sprintf("Provider %q runs on agent nodes, which only vagabond server can reach.",
					settings.Name),
			}}
		}

		p, diags := pool.New(settings.Name, settings.Config, settings.Nodes)
		if diags.HasErrors() {
			return nil, diags
		}

		return p, diags

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
