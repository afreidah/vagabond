// -------------------------------------------------------------------------------
// Credentialed Clients
//
// Author: Alex Freidah
//
// The client a provider signs its requests with, built from its credential and
// built again when a call is refused with 401 or 403 and the credential is
// resolved anew. A provider embeds Credentialed to get its Recredential method.
// -------------------------------------------------------------------------------

package plugin

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/hashicorp/hcl/v2"
)

// Credentialed holds the client of type C a provider builds from its
// credential, swapped whole whenever the credential is resolved again.
type Credentialed[C any] struct {
	build  func(ctx context.Context, credential []byte) (*C, error)
	client atomic.Pointer[C]
}

// NewCredentialed builds the first client from credential with build, or
// reports why provider name cannot use it.
func NewCredentialed[C any](
	ctx context.Context, name string, credential []byte,
	build func(ctx context.Context, credential []byte) (*C, error),
) (*Credentialed[C], hcl.Diagnostics) {
	c := &Credentialed[C]{build: build}

	if err := c.Recredential(ctx, credential); err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unusable credential",
			Detail:   fmt.Sprintf("Provider %q: %s.", name, err),
		}}
	}

	return c, nil
}

// Client returns the client built from the latest credential.
func (c *Credentialed[C]) Client() *C {
	return c.client.Load()
}

// Recredential builds the client again from a credential resolved anew. A
// client may fetch tokens long after this returns, so it is built on a context
// that outlives the call.
func (c *Credentialed[C]) Recredential(ctx context.Context, credential []byte) error {
	client, err := c.build(context.WithoutCancel(ctx), credential)
	if err != nil {
		return err
	}

	c.client.Store(client)

	return nil
}
