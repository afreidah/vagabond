// -------------------------------------------------------------------------------
// Lambda Provider
//
// Author: Alex Freidah
//
// One synchronous invoke per execution. The response carries the verdict, the
// last four kilobytes of log, and the REPORT line stating what AWS billed, so
// there is nothing to poll, fetch, cancel or clean up afterwards.
// -------------------------------------------------------------------------------

package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/quota"
)

// What Lambda will accept, published through Capabilities. CPU is derived from
// memory and cannot be chosen, so the ceiling is what 10 GB of memory brings.
const (
	maxCPU      = 6000
	maxMemory   = 10240
	maxDuration = 15 * time.Minute
)

// logTailSize is how much log Lambda returns with a synchronous invoke.
const logTailSize = 4096

// Provider dispatches function tasks to AWS Lambda.
type Provider struct {
	plugin.StatusNotSupported
	plugin.ResultNotSupported
	plugin.CancelNotSupported

	name   string
	client *lambda.Client
}

// New builds a Lambda provider from its configuration block.
func New(
	ctx context.Context, name string, body hcl.Body, credential []byte,
) (*Provider, hcl.Diagnostics) {
	cfg, diags := decodeConfig(name, body)
	if diags.HasErrors() {
		return nil, diags
	}

	client, err := newClient(ctx, cfg, credential)
	if err != nil {
		return nil, append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unusable credential",
			Detail:   fmt.Sprintf("Provider %q: %s.", name, err),
		})
	}

	return &Provider{name: name, client: client}, diags
}

// Name returns the routing identifier.
func (p *Provider) Name() string {
	return p.name
}

// Capabilities reports what Lambda can do. Constants: nothing here varies by
// account.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.Capabilities{
		Drivers:       []job.DriverName{job.DriverFunction},
		Architectures: []job.Arch{job.ArchAMD64, job.ArchARM64},

		MaxResources: plugin.Resources{CPU: maxCPU, Memory: maxMemory},
		MaxDuration:  maxDuration,

		InternetEgress:  true,
		ArbitraryImages: false,

		ObservedAt: time.Now(),
	}, nil
}

// Submit invokes the task's function and waits for it.
//
// A function error is the task failing, not the provider: the handler ran and
// said no. There is no exit code, so it becomes 1, and success 0.
func (p *Provider) Submit(
	ctx context.Context, id execution.ID, task *job.Task,
) (plugin.Submission, error) {
	function, payload, err := invocation(id, task)
	if err != nil {
		return plugin.Submission{}, err
	}

	started := time.Now()

	out, err := p.client.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   sdkaws.String(function),
		Payload:        payload,
		InvocationType: types.InvocationTypeRequestResponse,
		LogType:        types.LogTypeTail,
	})
	if err != nil {
		return plugin.Submission{}, classify(err)
	}

	result := &execution.Result{
		ID:       id,
		ExitCode: ptr.Of(0),
		Duration: time.Since(started),
	}

	state := execution.StateSucceeded
	if out.FunctionError != nil {
		state = execution.StateFailed
		result.ExitCode = ptr.Of(1)
	}

	if logs, err := base64.StdEncoding.DecodeString(sdkaws.ToString(out.LogResult)); err == nil {
		result.Logs = logs
		result.LogsTruncated = len(logs) >= logTailSize

		if r, ok := parseReport(string(logs)); ok {
			applyReport(result, r)
		}
	}

	return plugin.Submission{
		ProviderID: function,
		State:      state,
		Result:     result,
	}, nil
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
