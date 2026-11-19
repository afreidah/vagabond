// -------------------------------------------------------------------------------
// Provider Construction
//
// Author: Alex Freidah
//
// Maps a configured type to the plugin that implements it. This is the one
// place in the tree that knows every provider by name; depguard forbids the
// scheduler from importing a provider implementation. A new provider type is
// one row in builders.
// -------------------------------------------------------------------------------

package registry

import (
	"context"
	"fmt"
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

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Settings is everything a plugin constructor is handed.
//
// Config is the provider's block still undecoded, so a plugin can report a
// typo against its line and its field names stay out of internal/config.
// Credentials is already resolved to bytes, nil for a provider that needs
// none. Nodes is the server's connected agent nodes, which a node provider is
// made of; nil outside a server.
type Settings struct {
	Name        string
	Config      hcl.Body
	Credentials []byte
	Nodes       *nodes.Conns
}

// builder constructs one type's plugin from its settings. A failed build
// returns a nil interface, not one holding a nil pointer.
type builder func(ctx context.Context, settings Settings) (plugin.Provider, hcl.Diagnostics)

// builders is every type Build constructs, in the order errors list them.
var builders = []struct {
	providerType string
	build        builder
}{
	{gcp.Type, cloud(gcp.New)},
	{ibm.Type, cloud(ibm.New)},
	{aws.Type, cloud(aws.New)},
	{pool.TypeContainerd, onNodes(pool.TypeContainerd)},
	{pool.TypeFirecracker, onNodes(pool.TypeFirecracker)},

	{TypeFakeContainer, fake(plugin.NewFakeContainerProvider)},
	{TypeFakeFunction, fake(plugin.NewFakeFunctionProvider)},
	{TypeFakeWorker, fake(plugin.NewFakeWorkerProvider)},
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// Build constructs the plugin for a configured type under the configured name,
// so one type can be registered twice against two accounts. It returns
// diagnostics rather than an error so a config mistake keeps its source range.
func Build(
	ctx context.Context, providerType string, settings Settings,
) (plugin.Provider, hcl.Diagnostics) {
	known := make([]string, 0, len(builders))

	for _, b := range builders {
		if b.providerType == providerType {
			return b.build(ctx, settings)
		}

		known = append(known, b.providerType)
	}

	return nil, hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Unknown provider type",
		Detail: fmt.Sprintf("Provider %q is of type %q, which nothing implements. Known types are %s.",
			settings.Name, providerType, strings.Join(known, ", ")),
	}}
}

// cloud builds a provider on a cloud account from its block and credentials.
func cloud[P plugin.Provider](
	build func(context.Context, string, hcl.Body, []byte) (P, hcl.Diagnostics),
) builder {
	return func(ctx context.Context, s Settings) (plugin.Provider, hcl.Diagnostics) {
		p, diags := build(ctx, s.Name, s.Config, s.Credentials)
		if diags.HasErrors() {
			return nil, diags
		}

		return p, diags
	}
}

// onNodes builds a provider of providerType over the server's connected
// nodes, which only a server has.
func onNodes(providerType string) builder {
	return func(_ context.Context, s Settings) (plugin.Provider, hcl.Diagnostics) {
		if s.Nodes == nil {
			return nil, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Agent nodes without a server",
				Detail: fmt.Sprintf("Provider %q runs on agent nodes, which only vagabond server can reach.",
					s.Name),
			}}
		}

		p, diags := pool.New(s.Name, providerType, s.Config, s.Nodes)
		if diags.HasErrors() {
			return nil, diags
		}

		return p, diags
	}
}

// fake builds a fake provider from its name alone.
func fake[P plugin.Provider](build func(string) P) builder {
	return func(_ context.Context, s Settings) (plugin.Provider, hcl.Diagnostics) {
		return build(s.Name), nil
	}
}
