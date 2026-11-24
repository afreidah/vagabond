// -------------------------------------------------------------------------------
// Lambda Provider
//
// Author: Alex Freidah
//
// One synchronous invoke per execution. The function's reply carries the task's
// exit code and output, and the log tail the REPORT line stating what AWS
// billed, so there is nothing to poll, fetch, cancel or clean up afterwards.
// -------------------------------------------------------------------------------

package aws

import (
	"context"
	"encoding/base64"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// What Lambda accepts, published through Capabilities. CPU is derived from
// memory and cannot be chosen, so the ceiling is what 10 GB of memory brings.
const (
	maxCPU      = 6000
	maxMemory   = 10240
	maxDuration = 15 * time.Minute
)

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// Compile-time proof the contract is met.
var (
	_ plugin.Provider       = (*Provider)(nil)
	_ plugin.Recredentialer = (*Provider)(nil)
)

// Provider dispatches function tasks to AWS Lambda.
type Provider struct {
	plugin.StatusNotSupported
	plugin.ResultNotSupported
	plugin.CancelNotSupported
	*plugin.Credentialed[lambda.Client]

	name string
	cfg  *Config
}

// New builds a Lambda provider from its configuration block. The client is
// built again from a credential resolved anew whenever AWS rejects the one it
// holds, as it does an expired session token.
func New(
	ctx context.Context, name string, body hcl.Body, credential []byte,
) (*Provider, hcl.Diagnostics) {
	cfg, diags := decodeConfig(name, body)
	if diags.HasErrors() {
		return nil, diags
	}

	client, credDiags := plugin.NewCredentialed(ctx, name, credential,
		func(ctx context.Context, credential []byte) (*lambda.Client, error) {
			return newClient(ctx, cfg, credential)
		})
	if credDiags.HasErrors() {
		return nil, append(diags, credDiags...)
	}

	return &Provider{Credentialed: client, name: name, cfg: cfg}, diags
}

// -------------------------------------------------------------------------
// INVOCATION
// -------------------------------------------------------------------------

// Name returns the routing identifier.
func (p *Provider) Name() string {
	return p.name
}

// Capabilities reports what Lambda can do. Constants: nothing here varies by
// account. Each execution environment is a Firecracker microVM of its own.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.Capabilities{
		Drivers:       []job.DriverName{job.DriverFunction},
		Architectures: []job.Arch{job.ArchAMD64, job.ArchARM64},

		MaxResources: plugin.Limits{CPU: new(maxCPU), Memory: new(maxMemory)},
		MaxDuration:  maxDuration,

		InternetEgress:  true,
		ArbitraryImages: false,
		Isolation:       plugin.IsolationVM,

		ObservedAt: time.Now(),
	}, nil
}

// Submit invokes the task's function and waits for its reply. A handler that
// threw or timed out replies with Lambda's error instead, which fails the task
// with exit code 1 rather than the provider.
func (p *Provider) Submit(
	ctx context.Context, id execution.ID, task *job.Task,
) (plugin.Submission, error) {
	function, err := plugin.FunctionOf(id, task)
	if err != nil {
		return plugin.Submission{}, err
	}

	started := time.Now()

	out, err := p.Client().Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   sdkaws.String(function.Name),
		Payload:        function.Event,
		InvocationType: types.InvocationTypeRequestResponse,
		LogType:        types.LogTypeTail,
	})
	if err != nil {
		return plugin.Submission{}, classify(err)
	}

	sub := plugin.Replied(id, function.Name, out.Payload, time.Since(started))

	// The log tail is read only for the REPORT line closing it
	if logs, err := base64.StdEncoding.DecodeString(sdkaws.ToString(out.LogResult)); err == nil {
		if r, ok := parseReport(string(logs)); ok {
			applyReport(sub.Result, r)
		}
	}

	return sub, nil
}

// applyReport replaces the measured duration with Lambda's, and records the
// bill when the line stated one. Without it the ledger prices the declared
// shape over the duration, which is the estimate rather than nothing.
func applyReport(result *execution.Result, r report) {
	if r.duration > 0 {
		result.Duration = r.duration
	}

	if r.hasBill() {
		result.Billed = &quota.Execution{Memory: r.memory, Duration: r.billed}
	}
}
