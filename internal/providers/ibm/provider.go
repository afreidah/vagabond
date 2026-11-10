// -------------------------------------------------------------------------------
// Code Engine Jobs Provider
//
// Author: Alex Freidah
//
// The Provider methods against Code Engine. A task runs as one job run named
// from the execution id, so every later call derives its target and nothing is
// remembered between calls. The run's status comes from the v2 API; its exit
// code and output come from its pod, which deleting the run takes with it.
// -------------------------------------------------------------------------------

package ibm

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// namePrefix marks every job run this plugin creates. The execution id follows
// it, which is how a later process finds and releases a run it did not submit.
const namePrefix = "vagabond-"

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Compile-time proof the contract is met.
var (
	_ plugin.Provider       = (*Provider)(nil)
	_ plugin.LogStreamer    = (*Provider)(nil)
	_ plugin.Releaser       = (*Provider)(nil)
	_ plugin.Recredentialer = (*Provider)(nil)
)

// Provider dispatches container tasks to Code Engine job runs. The endpoints
// and the pod poll are fields so a test can point the plugin at a local server
// and not wait on it.
type Provider struct {
	name string
	cfg  *Config
	auth atomic.Pointer[session] // swapped whole when the credential is refreshed
	ns   atomic.Pointer[string]  // the project's namespace, once read

	iamURL  string
	apiURL  string
	kubeURL string
	podPoll time.Duration
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New builds a Code Engine provider from its configuration block, returning
// diagnostics so a bad field points at the line holding it.
func New(
	ctx context.Context, name string, body hcl.Body, credentials []byte,
) (*Provider, hcl.Diagnostics) {
	cfg, diags := decodeConfig(name, body)
	if diags.HasErrors() {
		return nil, diags
	}

	auth, err := newSession(ctx, iamEndpoint, credentials)
	if err != nil {
		return nil, append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unusable credential",
			Detail:   fmt.Sprintf("Provider %q: %s.", name, err),
		})
	}

	p := &Provider{
		name:    name,
		cfg:     cfg,
		iamURL:  iamEndpoint,
		apiURL:  apiBase(cfg.Region),
		kubeURL: kubeBase(cfg.Region),
		podPoll: defaultPodPoll,
	}
	p.auth.Store(auth)

	return p, diags
}

// Recredential rebuilds the session from a key resolved again, after a call
// was refused with 401 or 403. Tokens are fetched long after this call
// returns, so the session is built on a context that outlives it.
func (p *Provider) Recredential(ctx context.Context, credential []byte) error {
	auth, err := newSession(context.WithoutCancel(ctx), p.iamURL, credential)
	if err != nil {
		return err
	}

	p.auth.Store(auth)

	return nil
}

// Name returns the routing identifier.
func (p *Provider) Name() string {
	return p.name
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// Capabilities reports what Code Engine can do: constants, since nothing here
// varies by account. ObservedAt is set anyway, as admission requires.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.CloudContainer(maxCPU, maxMemory, maxDuration), nil
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// Submit creates the job run for this execution, image and settings inline, so
// no job has to exist first. A second Submit with the same id collides on the
// name rather than starting a second run.
func (p *Provider) Submit(
	ctx context.Context, id execution.ID, task *job.Task,
) (plugin.Submission, error) {
	name := runName(id)

	spec, err := p.buildSpec(name, task)
	if err != nil {
		return plugin.Submission{}, err
	}

	if _, err := p.createJobRun(ctx, spec); err != nil {
		return plugin.Submission{}, err
	}

	return plugin.Submission{
		ProviderID: name,
		State:      execution.StateAccepted,
	}, nil
}

// -------------------------------------------------------------------------
// STATUS
// -------------------------------------------------------------------------

// Status reports where an execution has reached, with the run's own times
// rather than its instance's: the instance finishes seconds before the run
// reports it, and an end time should not appear on a run still running.
func (p *Provider) Status(
	ctx context.Context, id execution.ID,
) (execution.Status, error) {
	name := runName(id)

	run, err := p.getJobRun(ctx, name)
	if err != nil {
		return execution.Status{}, err
	}

	state, err := run.state()
	if err != nil {
		return execution.Status{}, err
	}

	status := execution.Status{
		ID:         id,
		State:      state,
		ProviderID: name,
		StartedAt:  run.started(),
		EndedAt:    run.ended(),
	}

	status.UpdatedAt = status.EndedAt
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = time.Now()
	}

	return status, nil
}

// -------------------------------------------------------------------------
// RESULT
// -------------------------------------------------------------------------

// Result returns the exit code and output from the run's pod. A pod already
// gone leaves the exit code Code Engine recorded and no output; a log that
// cannot be read leaves the exit code alone, since it is still an answer.
func (p *Provider) Result(
	ctx context.Context, id execution.ID,
) (*execution.Result, error) {
	name := runName(id)

	run, err := p.getJobRun(ctx, name)
	if err != nil {
		return nil, err
	}

	found, err := p.runPod(ctx, name)
	if err != nil {
		return nil, err
	}

	instance := run.instance()
	result := &execution.Result{
		ID:       id,
		ExitCode: run.failureExit(),
	}

	if !instance.started().IsZero() && !instance.finished().IsZero() {
		result.Duration = instance.finished().Sub(instance.started())
	}

	if found == nil {
		return result, nil
	}

	if code := found.exitCode(); code != nil {
		result.ExitCode = code
	}

	logs, truncated, err := p.podLogs(ctx, found.Metadata.Name)
	if err == nil {
		result.Logs = logs
		result.LogsTruncated = truncated
	}

	return result, nil
}

// -------------------------------------------------------------------------
// CANCEL
// -------------------------------------------------------------------------

// Cancel stops an execution by deleting its job run: Code Engine has no other
// way to stop one. The pod goes with it, so this destroys what Result reads.
func (p *Provider) Cancel(ctx context.Context, id execution.ID) error {
	return p.deleteJobRun(ctx, runName(id))
}

// Release deletes the job run a finished execution left behind, which
// otherwise counts against the project's limit of 100. Dispatch calls it after
// the result, and the release loop for every other ending.
func (p *Provider) Release(ctx context.Context, id execution.ID) error {
	return p.Cancel(ctx, id)
}

// -------------------------------------------------------------------------
// NAMING
// -------------------------------------------------------------------------

// runName is the job run for an execution: 45 characters, inside Code Engine's
// 53, lowercase as its naming rules require.
func runName(id execution.ID) string {
	return namePrefix + id.String()
}
