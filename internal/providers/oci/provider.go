// -------------------------------------------------------------------------------
// OCI Functions Provider
//
// Author: Alex Freidah
//
// One synchronous invoke per execution. The function is looked up first for
// where it is invoked and the memory it is billed at; its reply carries the
// task's exit code and output, so there is nothing to poll, fetch, cancel or
// clean up afterwards.
// -------------------------------------------------------------------------------

package oci

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/rest"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// What OCI Functions accepts, published through Capabilities: its largest
// memory setting and the longest synchronous invoke. Oracle publishes no CPU
// figure, so no CPU limit is advertised.
const (
	maxMemory   = 3072
	maxDuration = 300 * time.Second
)

// maxReply is the largest reply OCI returns from an invoke.
const maxReply = 6 << 20

// requestIDHeader carries the ID OCI handled an invoke under, which the
// function's logs in OCI Logging are searched by.
const requestIDHeader = "opc-request-id"

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// Compile-time proof the contract is met.
var (
	_ plugin.Provider       = (*Provider)(nil)
	_ plugin.Recredentialer = (*Provider)(nil)
)

// Provider dispatches function tasks to OCI Functions. The endpoint is the
// region's Functions API, a field so a test can point it at a local server.
type Provider struct {
	plugin.StatusNotSupported
	plugin.ResultNotSupported
	plugin.CancelNotSupported
	*plugin.Credentialed[http.Client]

	name     string
	endpoint string
}

// New builds an OCI Functions provider from its configuration block and the
// private key its requests are signed with. The client is built again from a
// key resolved anew whenever OCI rejects the one it holds.
func New(
	ctx context.Context, name string, body hcl.Body, credential []byte,
) (*Provider, hcl.Diagnostics) {
	cfg, diags := decodeConfig(name, body)
	if diags.HasErrors() {
		return nil, diags
	}

	client, credDiags := plugin.NewCredentialed(ctx, name, credential,
		func(_ context.Context, key []byte) (*http.Client, error) {
			return newHTTPClient(cfg, key)
		})
	if credDiags.HasErrors() {
		return nil, append(diags, credDiags...)
	}

	return &Provider{Credentialed: client, name: name, endpoint: managementEndpoint(cfg.Region)}, diags
}

// -------------------------------------------------------------------------
// INVOCATION
// -------------------------------------------------------------------------

// Name returns the routing identifier.
func (p *Provider) Name() string {
	return p.name
}

// Capabilities reports what OCI Functions can do. Constants: nothing here
// varies by account. A function's shape decides whether it runs on x86, Arm or
// either, so both are offered.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.Capabilities{
		Drivers:       []job.DriverName{job.DriverFunction},
		Architectures: []job.Arch{job.ArchAMD64, job.ArchARM64},

		MaxResources: plugin.Limits{Memory: new(maxMemory)},
		MaxDuration:  maxDuration,

		InternetEgress:  true,
		ArbitraryImages: false,

		ObservedAt: time.Now(),
	}, nil
}

// Submit looks the task's function up, invokes it and waits for its reply. A
// function that crashed or ran past its timeout answers with OCI's error
// instead, which fails the task with exit code 1 rather than the provider.
func (p *Provider) Submit(
	ctx context.Context, id execution.ID, task *job.Task,
) (plugin.Submission, error) {
	function, err := plugin.FunctionOf(id, task)
	if err != nil {
		return plugin.Submission{}, err
	}

	fn, err := p.lookup(ctx, function.Name)
	if err != nil {
		return plugin.Submission{}, err
	}

	started := time.Now()

	reply, requestID, err := p.invoke(ctx, fn, function)
	if err != nil {
		return plugin.Submission{}, err
	}

	sub := plugin.Replied(id, cmp.Or(requestID, function.Name), reply, time.Since(started))
	sub.Result.Billed = &quota.Execution{Memory: fn.MemoryInMBs, Duration: sub.Result.Duration}

	return sub, nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// deployed is what an invoke needs of a function's configuration: where it is
// invoked, and the memory OCI bills it at.
type deployed struct {
	InvokeEndpoint string `json:"invokeEndpoint"`
	MemoryInMBs    int    `json:"memoryInMBs"`
}

// lookup reads the function's configuration from the Functions API. Every
// failure is classified.
func (p *Provider) lookup(ctx context.Context, ocid string) (deployed, error) {
	var fn deployed

	err := rest.Call(ctx, p.Client(), &rest.Request{
		Method: http.MethodGet,
		URL:    p.endpoint + "/" + apiVersion + "/functions/" + url.PathEscape(ocid),
	}, &fn)

	return fn, plugin.ClassifyResponse(err, refused)
}

// invoke sends the function its event and returns its reply with the ID OCI
// handled it under. A refusal naming a function fault is the reply, since the
// function ran; any other failure is classified.
func (p *Provider) invoke(ctx context.Context, fn deployed, function plugin.Function) ([]byte, string, error) {
	resp, err := rest.Open(ctx, p.Client(), &rest.Request{
		Method:      http.MethodPost,
		URL:         fn.InvokeEndpoint + "/" + apiVersion + "/functions/" + url.PathEscape(function.Name) + "/actions/invoke",
		Body:        bytes.NewReader(function.Event),
		ContentType: "application/json",
	})

	if r, ok := errors.AsType[*rest.Refusal](err); ok && functionFailed(r) {
		return r.Body, r.Header.Get(requestIDHeader), nil
	}

	if err != nil {
		return nil, "", plugin.ClassifyResponse(err, refused)
	}

	defer func() { _ = resp.Body.Close() }()

	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return nil, "", plugin.Infrastructure(fmt.Errorf("reading the reply: %w", err))
	}

	return reply, resp.Header.Get(requestIDHeader), nil
}
