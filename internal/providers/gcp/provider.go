// -------------------------------------------------------------------------------
// Cloud Run Jobs Provider
//
// Author: Alex Freidah
//
// The Provider methods against Cloud Run's Admin API. Four things here are not
// obvious and each is commented where it happens: Vagabond names the job but
// Google names the execution, every run leaves a resource behind, cleanup
// destroys what Result reads, and startup dominates the time a run takes.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

// namePrefix marks every job this plugin creates. The execution id follows it,
// which is how a later process finds and releases a run it did not submit.
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

// Provider dispatches container tasks to Cloud Run Jobs. The endpoints are
// fields so a test can point the plugin at a local server.
type Provider struct {
	name string
	cfg  *Config
	http atomic.Pointer[http.Client] // swapped whole when the credential is refreshed

	runURL  string
	logsURL string
}

// New builds a Cloud Run provider from its configuration block, returning
// diagnostics so a bad field points at the line holding it.
func New(
	ctx context.Context, name string, body hcl.Body, credentials []byte,
) (*Provider, hcl.Diagnostics) {
	cfg, diags := decodeConfig(name, body)
	if diags.HasErrors() {
		return nil, diags
	}

	client, err := newHTTPClient(ctx, credentials)
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
		runURL:  runEndpoint,
		logsURL: loggingEndpoint,
	}
	p.http.Store(client)

	return p, diags
}

// Recredential rebuilds the HTTP client from a key resolved again, after an
// Admin API call returned 401 or 403. The client fetches tokens on the context
// it was built with, so that context outlives the call asking for the refresh.
func (p *Provider) Recredential(ctx context.Context, credential []byte) error {
	client, err := newHTTPClient(context.WithoutCancel(ctx), credential)
	if err != nil {
		return err
	}

	p.http.Store(client)

	return nil
}

// Name returns the routing identifier.
func (p *Provider) Name() string {
	return p.name
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// Capabilities reports what Cloud Run can do: constants, since nothing here
// varies by account. ObservedAt is set anyway, as admission requires.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.CloudContainer(maxCPU, maxMemory, maxDuration), nil
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// Submit creates a job for this execution and runs it: Cloud Run has no ad-hoc
// form, so a Job must exist before an Execution of it can start.
//
// The job is named from the execution id, so any process can derive every
// later call without this plugin remembering anything.
func (p *Provider) Submit(
	ctx context.Context, id execution.ID, task *job.Task,
) (plugin.Submission, error) {
	name := jobName(id)

	spec, err := p.jobSpec(task)
	if err != nil {
		return plugin.Submission{}, err
	}

	// A retry with the same id collides on the job name, not a second run
	err = p.call(ctx, http.MethodPost,
		p.cfg.jobsURL(p.runURL)+"?jobId="+name, spec, nil)
	if err != nil {
		return plugin.Submission{}, err
	}

	if err := p.call(ctx, http.MethodPost, p.cfg.jobURL(p.runURL, name)+":run", struct{}{}, nil); err != nil {
		// Nothing runs the job, so delete it now rather than at release
		p.deleteJob(ctx, name)

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

// Status reports where an execution has reached. Cloud Run names the execution
// with a suffix of its own, so it is listed rather than derived from the id.
func (p *Provider) Status(
	ctx context.Context, id execution.ID,
) (execution.Status, error) {
	name := jobName(id)

	found, err := p.execution(ctx, name)
	if err != nil {
		return execution.Status{}, err
	}

	status := execution.Status{
		ID:         id,
		State:      found.state(),
		ProviderID: name,
		StartedAt:  parseTime(found.StartTime),
		EndedAt:    parseTime(found.CompletionTime),
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

// Result returns the exit code from the task and the output from Cloud Logging.
// Output is best effort: an exit code with no readable logs is still an answer.
func (p *Provider) Result(
	ctx context.Context, id execution.ID,
) (*execution.Result, error) {
	name := jobName(id)

	found, err := p.execution(ctx, name)
	if err != nil {
		return nil, err
	}

	task, err := p.task(ctx, found.Name)
	if err != nil {
		return nil, err
	}

	result := &execution.Result{
		ID:       id,
		ExitCode: task.exitCode(),
		Duration: parseTime(task.CompletionTime).Sub(parseTime(task.StartTime)),
	}

	logs, truncated, err := p.logs(ctx, name)
	if err == nil {
		result.Logs = logs
		result.LogsTruncated = truncated
	}

	return result, nil
}

// -------------------------------------------------------------------------
// CANCEL
// -------------------------------------------------------------------------

// Cancel stops an execution by deleting its job, which takes its executions and
// tasks with it and so destroys what Result reads. A job already gone is not an
// error.
func (p *Provider) Cancel(ctx context.Context, id execution.ID) error {
	err := p.call(ctx, http.MethodDelete, p.cfg.jobURL(p.runURL, jobName(id)), nil, nil)
	if isNotFound(err) {
		return nil
	}

	return err
}

// Release deletes the Job a finished execution left behind, which otherwise
// counts against a per-region quota. Dispatch calls it after the result, and
// the release loop for every other ending.
func (p *Provider) Release(ctx context.Context, id execution.ID) error {
	return p.Cancel(ctx, id)
}

// deleteJob removes a job, ignoring the error: it runs on paths already
// returning a failure, and that failure is the one worth reporting.
func (p *Provider) deleteJob(ctx context.Context, name string) {
	_ = p.call(ctx, http.MethodDelete, p.cfg.jobURL(p.runURL, name), nil, nil)
}

// -------------------------------------------------------------------------
// NAMING
// -------------------------------------------------------------------------

// jobName is the Cloud Run job for an execution: 45 characters, inside Cloud
// Run's 63, and starting with a letter as its naming rules require.
func jobName(id execution.ID) string {
	return namePrefix + id.String()
}

// isNotFound reports whether an error is Google saying the resource is gone.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "http 404")
}
