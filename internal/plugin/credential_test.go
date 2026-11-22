// -------------------------------------------------------------------------------
// Credentialed Client Tests
//
// Author: Alex Freidah
//
// The first client is built from the credential or the provider is refused
// with a diagnostic naming it, a credential resolved again replaces the client
// only when it builds, and the client outlives the call that built it.
// -------------------------------------------------------------------------------

package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// client is what the tests build: the credential it came from, and the
// context it was built on.
type client struct {
	credential string
	ctx        context.Context
}

// build makes a client from any credential but "bad".
func build(ctx context.Context, credential []byte) (*client, error) {
	if string(credential) == "bad" {
		return nil, errors.New("not a key")
	}

	return &client{credential: string(credential), ctx: ctx}, nil
}

// A usable credential builds the first client; an unusable one is refused
// with a diagnostic naming the provider and the reason.
func TestNewCredentialed(t *testing.T) {
	c, diags := NewCredentialed(t.Context(), "box", []byte("first"), build)
	if diags.HasErrors() || c.Client().credential != "first" {
		t.Fatalf("NewCredentialed() = %+v, %s; want the first client", c, diags.Error())
	}

	_, diags = NewCredentialed(t.Context(), "box", []byte("bad"), build)
	if !diags.HasErrors() || diags[0].Summary != "Unusable credential" ||
		!strings.Contains(diags[0].Detail, `"box"`) || !strings.Contains(diags[0].Detail, "not a key") {
		t.Errorf("NewCredentialed(bad) = %v, want Unusable credential naming box and why", diags)
	}
}

// A credential resolved again replaces the client, a bad one leaves the last
// good client in place, and either way the client outlives the call's context.
func TestCredentialed_Recredential(t *testing.T) {
	c, _ := NewCredentialed(t.Context(), "box", []byte("first"), build)

	ctx, cancel := context.WithCancel(t.Context())

	if err := c.Recredential(ctx, []byte("second")); err != nil {
		t.Fatalf("Recredential() = %v", err)
	}

	cancel()

	if got := c.Client(); got.credential != "second" || got.ctx.Err() != nil {
		t.Errorf("client = %q, context err %v; want second, built on a context that outlives the call",
			got.credential, got.ctx.Err())
	}

	if err := c.Recredential(t.Context(), []byte("bad")); err == nil {
		t.Error("Recredential(bad) = nil, want the build error")
	}

	if got := c.Client().credential; got != "second" {
		t.Errorf("client after a failed Recredential = %q, want second", got)
	}
}
