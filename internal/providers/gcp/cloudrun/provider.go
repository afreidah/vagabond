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

package cloudrun

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/providers/gcp"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Type is what a provider block names to select Cloud Run.
const Type = "cloud-run"

// runEndpoint is Cloud Run's Admin API.
const runEndpoint = "https://run.googleapis.com"

// Compile-time proof the contract is met.
var (
	_ plugin.Provider       = (*Provider)(nil)
	_ plugin.LogStreamer    = (*Provider)(nil)
	_ plugin.LogReader      = (*Provider)(nil)
	_ plugin.Releaser       = (*Provider)(nil)
	_ plugin.Recredentialer = (*Provider)(nil)
)

// Provider dispatches container tasks to Cloud Run Jobs. The endpoint is a
// field so a test can point the plugin at a local server.
type Provider struct {
	*gcp.API

	runURL string
}

// New builds a Cloud Run provider from its configuration block, returning
// diagnostics so a bad field points at the line holding it.
func New(
	ctx context.Context, name string, body hcl.Body, credentials []byte,
) (*Provider, hcl.Diagnostics) {
	api, diags := gcp.NewAPI(ctx, name, Type, body, credentials)
	if diags.HasErrors() {
		return nil, diags
	}

	return &Provider{API: api, runURL: runEndpoint}, diags
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// Capabilities reports what Cloud Run can do: constants, since nothing here
// varies by account. ObservedAt is set anyway, as admission requires.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.CloudContainer(platform), nil
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
	name := plugin.ResourceName(id)

	spec, err := p.jobSpec(task)
	if err != nil {
		return plugin.Submission{}, err
	}

	// A retry with the same id collides on the job name, not a second run
	if err := p.Call(ctx, http.MethodPost, p.jobsURL()+"?jobId="+name, spec, nil); err != nil {
		return plugin.Submission{}, err
	}

	if err := p.Call(ctx, http.MethodPost, p.jobURL(name)+":run", struct{}{}, nil); err != nil {
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
	name := plugin.ResourceName(id)

	found, err := p.execution(ctx, name)
	if err != nil {
		return execution.Status{}, err
	}

	return plugin.ObservedStatus(id, found.state(), name,
		plugin.ParseTime(found.StartTime), plugin.ParseTime(found.CompletionTime)), nil
}

// -------------------------------------------------------------------------
// RESULT
// -------------------------------------------------------------------------

// Result returns the exit code and run time from the task. The output is read
// apart, by ReadLogs.
func (p *Provider) Result(
	ctx context.Context, id execution.ID,
) (*execution.Result, error) {
	name := plugin.ResourceName(id)

	found, err := p.execution(ctx, name)
	if err != nil {
		return nil, err
	}

	task, err := p.task(ctx, found.Name)
	if err != nil {
		return nil, err
	}

	return &execution.Result{
		ID:       id,
		ExitCode: task.exitCode(),
		Duration: execution.Elapsed(plugin.ParseTime(task.StartTime), plugin.ParseTime(task.CompletionTime)),
	}, nil
}

// -------------------------------------------------------------------------
// OUTPUT
// -------------------------------------------------------------------------

// ReadLogs writes what a finished job's container printed. The entries outlive
// the job, so Release destroys nothing this reads.
func (p *Provider) ReadLogs(ctx context.Context, id execution.ID, w execution.LogWriter) error {
	return p.ReadEntries(ctx, jobLogs(id), w)
}

// StreamLogs writes a running job's output to w from byte offset on, as it is
// logged.
func (p *Provider) StreamLogs(ctx context.Context, id execution.ID, offset int64, w io.Writer) error {
	return p.StreamEntries(ctx, jobLogs(id), offset, w)
}

// jobLogs is the filter picking out the output of an execution's job.
func jobLogs(id execution.ID) string {
	return fmt.Sprintf(`resource.type="cloud_run_job" resource.labels.job_name=%q`, plugin.ResourceName(id))
}

// -------------------------------------------------------------------------
// CANCEL
// -------------------------------------------------------------------------

// Cancel stops an execution by deleting its job, which takes its executions and
// tasks with it and so destroys what Result reads. A job already gone is not an
// error.
func (p *Provider) Cancel(ctx context.Context, id execution.ID) error {
	return plugin.IgnoreNotFound(p.Call(ctx, http.MethodDelete, p.jobURL(plugin.ResourceName(id)), nil, nil))
}

// Release deletes the Job a finished execution left behind, which otherwise
// counts against a per-region quota. The execution's runner calls it last,
// however the execution ended.
func (p *Provider) Release(ctx context.Context, id execution.ID) error {
	return p.Cancel(ctx, id)
}

// deleteJob removes a job, ignoring the error: it runs on paths already
// returning a failure, and that failure is the one worth reporting.
func (p *Provider) deleteJob(ctx context.Context, name string) {
	_ = p.Call(ctx, http.MethodDelete, p.jobURL(name), nil, nil)
}

// -------------------------------------------------------------------------
// ENDPOINTS
// -------------------------------------------------------------------------

// jobsURL is the collection every job of this provider lives in, under a base
// the provider holds so a test can point it at a local server.
func (p *Provider) jobsURL() string {
	return fmt.Sprintf("%s/v2/projects/%s/locations/%s/jobs", p.runURL, p.Config.Project, p.Config.Region)
}

// jobURL addresses one job by name, the name Submit gave it from the
// execution id.
func (p *Provider) jobURL(job string) string {
	return p.jobsURL() + "/" + job
}
