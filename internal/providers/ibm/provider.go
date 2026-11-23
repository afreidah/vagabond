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
	"sync/atomic"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Compile-time proof the contract is met.
var (
	_ plugin.Provider       = (*Provider)(nil)
	_ plugin.LogStreamer    = (*Provider)(nil)
	_ plugin.LogReader      = (*Provider)(nil)
	_ plugin.Releaser       = (*Provider)(nil)
	_ plugin.Recredentialer = (*Provider)(nil)
)

// Provider dispatches container tasks to Code Engine job runs. The endpoints
// and the pod poll are fields so a test can point the plugin at a local server
// and not wait on it.
type Provider struct {
	*plugin.Credentialed[session]

	name string
	cfg  *Config
	ns   atomic.Pointer[string] // the project's namespace, once read

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

	p := &Provider{
		name:    name,
		cfg:     cfg,
		iamURL:  iamEndpoint,
		apiURL:  apiBase(cfg.Region),
		kubeURL: kubeBase(cfg.Region),
		podPoll: defaultPodPoll,
	}

	auth, credDiags := plugin.NewCredentialed(ctx, name, credentials, p.signIn)
	if credDiags.HasErrors() {
		return nil, append(diags, credDiags...)
	}

	p.Credentialed = auth

	return p, diags
}

// signIn builds the session an API key is exchanged through, against the IAM
// endpoint the provider holds, at construction and on every key resolved again.
func (p *Provider) signIn(ctx context.Context, key []byte) (*session, error) {
	return newSession(ctx, p.iamURL, key)
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
	return plugin.CloudContainer(codeEngine), nil
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
	name := plugin.ResourceName(id)

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
	name := plugin.ResourceName(id)

	run, err := p.getJobRun(ctx, name)
	if err != nil {
		return execution.Status{}, err
	}

	state, err := run.state()
	if err != nil {
		return execution.Status{}, err
	}

	return plugin.ObservedStatus(id, state, name, run.started(), run.ended()), nil
}

// -------------------------------------------------------------------------
// RESULT
// -------------------------------------------------------------------------

// Result returns the exit code and the platform's reason from the run's pod. A
// pod already gone leaves what Code Engine recorded. The output is read apart,
// by ReadLogs.
func (p *Provider) Result(
	ctx context.Context, id execution.ID,
) (*execution.Result, error) {
	name := plugin.ResourceName(id)

	run, err := p.getJobRun(ctx, name)
	if err != nil {
		return nil, err
	}

	found, err := p.runPod(ctx, name)
	if err != nil {
		return nil, err
	}

	code, reason := run.lastFailure()
	result := &execution.Result{
		ID:       id,
		ExitCode: code,
		Duration: run.ran(),
		Reason:   reason,
	}

	if found == nil {
		return result, nil
	}

	// The pod's detail, where it has some, is better than the run's summary
	if code := found.exitCode(); code != nil {
		result.ExitCode = code
	}

	if reason := found.reason(); reason != "" {
		result.Reason = reason
	}

	return result, nil
}

// -------------------------------------------------------------------------
// CANCEL
// -------------------------------------------------------------------------

// Cancel stops an execution by deleting its job run: Code Engine has no other
// way to stop one. The pod goes with it, so this destroys what Result and
// ReadLogs read.
func (p *Provider) Cancel(ctx context.Context, id execution.ID) error {
	return p.deleteJobRun(ctx, plugin.ResourceName(id))
}

// Release deletes the job run a finished execution left behind, which
// otherwise counts against the project's limit of 100. The execution's runner
// calls it last, however the execution ended.
func (p *Provider) Release(ctx context.Context, id execution.ID) error {
	return p.Cancel(ctx, id)
}
