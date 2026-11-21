// -------------------------------------------------------------------------------
// Memory Disabled Providers
//
// Author: Alex Freidah
//
// The providers turned off through the API, kept in a map for a server started
// with -dev and for tests. Follows the same rules as the Postgres store and is
// lost with the process.
// -------------------------------------------------------------------------------

package memory

import (
	"context"
	"maps"
	"sync"

	"github.com/afreidah/vagabond/internal/state"
)

// Providers holds every disabled provider by name.
type Providers struct {
	mu       sync.Mutex
	disabled map[string]state.Disabled
}

// NewProviders returns a store in which no provider is disabled.
func NewProviders() *Providers {
	return &Providers{disabled: make(map[string]state.Disabled)}
}

// DisableProvider stores that an operator turned the named provider off. If
// it is already off, the stored time and name are kept.
func (p *Providers) DisableProvider(_ context.Context, name string, d state.Disabled) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.disabled[name]; !ok {
		p.disabled[name] = d
	}

	return nil
}

// EnableProvider deletes the named provider's record, turning it back on. A
// provider that was not disabled is left alone.
func (p *Providers) EnableProvider(_ context.Context, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.disabled, name)

	return nil
}

// DisabledProviders returns a copy of every disabled provider, keyed by name.
// An empty map means none is.
func (p *Providers) DisabledProviders(context.Context) (map[string]state.Disabled, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return maps.Clone(p.disabled), nil
}
