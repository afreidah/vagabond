// -------------------------------------------------------------------------------
// Cloud Run Jobs Provider
//
// Author: Alex Freidah
//
// The five methods, against the API a spike proved before any of this was
// written. Four things here are not obvious and each is commented where it
// happens: we name the job but Google names the execution, every run leaves a
// resource behind, cleanup destroys what Result reads, and startup dominates
// the time a run takes.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// namePrefix marks every job this plugin creates.
//
// Load-bearing twice over: it carries our execution id, which is how a later
// process finds a run it did not submit, and it identifies our leftovers to
// the sweep without touching anything else in the project.
const namePrefix = "vagabond-"

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Provider dispatches container tasks to Cloud Run Jobs.
//
// The endpoints are fields rather than constants so a test can point the whole
// plugin at a local server and exercise real request building, encoding and
// status handling, rather than asserting that a mock was called correctly.
type Provider struct {
	name string
	cfg  *Config
	http *http.Client

	runURL  string
	logsURL string
}

// New builds a Cloud Run provider from its configuration block.
//
// Diagnostics rather than an error, because the config block is HCL the
// operator wrote and a bad field should point at the line holding it.
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

	return &Provider{
		name:    name,
		cfg:     cfg,
		http:    client,
		runURL:  runEndpoint,
		logsURL: loggingEndpoint,
	}, diags
}

// Name returns the routing identifier.
func (p *Provider) Name() string {
	return p.name
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// Capabilities reports what Cloud Run can do.
//
// Constants rather than a call. Nothing here varies by account or over time,
// and asking Google what Cloud Run is would be a network round trip to learn
// something the documentation already fixes.
//
// ObservedAt is set anyway, because admission reads an unobserved snapshot as
// a provider nothing is known about.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.Capabilities{
		Drivers:       []job.DriverName{job.DriverContainer},
		Architectures: []job.Arch{job.ArchAMD64},

		MaxResources: plugin.Resources{CPU: maxCPU, Memory: maxMemory},
		MaxDuration:  maxDuration,

		InternetEgress:  true,
		ArbitraryImages: true,

		ObservedAt: time.Now(),
	}, nil
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// Submit creates a job for this execution and runs it.
//
// Two calls, because Cloud Run has no ad-hoc form: a Job is a resource that
// must exist before an Execution of it can start. That is the cost of this
// platform and the reason Cancel has a sweep behind it.
//
// The job is named from the execution id, which is what makes every later call
// derivable without this plugin remembering anything. A CLI process that
// submits and exits leaves an execution a different process can still ask
// about.
func (p *Provider) Submit(
	ctx context.Context, id execution.ID, task *job.Task,
) (plugin.Submission, error) {
	name := jobName(id)

	spec, err := p.jobSpec(task)
	if err != nil {
		return plugin.Submission{}, err
	}

	// Idempotent by construction: a retry with the same id collides on the job
	// name and Google refuses it, rather than starting a second run.
	err = p.call(ctx, http.MethodPost,
		p.cfg.jobsURL(p.runURL)+"?jobId="+name, spec, nil)
	if err != nil {
		return plugin.Submission{}, err
	}

	if err := p.call(ctx, http.MethodPost, p.cfg.jobURL(p.runURL, name)+":run", struct{}{}, nil); err != nil {
		// The job exists but nothing is running it, so clean up rather than
		// leaving a resource the sweep has to notice later.
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

// Status reports where an execution has reached.
//
// Cloud Run generates the execution's name with a suffix of its own, so it
// cannot be derived from our id and has to be listed. There is exactly one,
// because Submit creates a job per execution.
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

// Result returns the exit code and whatever the container printed.
//
// The two halves come from different services. The exit code is a structured
// integer on the task, which is the reason this platform was chosen over Code
// Engine; the output comes from Cloud Logging, which is queried separately and
// has its own free tier.
//
// Output is best effort. A run that produced an exit code and no readable logs
// is still a usable answer, and failing the whole result because a log query
// was refused would discard the part that matters.
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

// Cancel stops an execution by deleting its job.
//
// Deleting the job takes its executions and tasks with it, which is also how
// the resource Submit created gets cleaned up. It therefore destroys what
// Result reads, so a caller wanting both fetches the result first; the
// interface says so.
//
// A job that is already gone is not an error. The caller wanted it not
// running, and it is not.
func (p *Provider) Cancel(ctx context.Context, id execution.ID) error {
	err := p.call(ctx, http.MethodDelete, p.cfg.jobURL(p.runURL, jobName(id)), nil, nil)
	if isNotFound(err) {
		return nil
	}

	return err
}

// Release deletes the Job left behind by a finished execution.
//
// The same delete as Cancel, reached for a different reason: nothing is
// running, and this is the resource that outlived it. Without this every run
// leaves a Job against a per-region quota, and Sweep becomes the only thing
// keeping the project usable rather than the backstop it is meant to be.
func (p *Provider) Release(ctx context.Context, id execution.ID) error {
	return p.Cancel(ctx, id)
}

// deleteJob removes a job, ignoring whether it worked.
//
// Used on paths already returning a failure, where the delete is tidying up
// after something that has gone wrong and its own error would replace the one
// worth reporting.
func (p *Provider) deleteJob(ctx context.Context, name string) {
	_ = p.call(ctx, http.MethodDelete, p.cfg.jobURL(p.runURL, name), nil, nil)
}

// -------------------------------------------------------------------------
// NAMING
// -------------------------------------------------------------------------

// jobName is the Cloud Run job for an execution.
//
// A UUIDv7 is 36 lowercase hex characters and hyphens, which with the prefix
// is 45 and inside Cloud Run's 63 character limit. It starts with a letter
// because the prefix does, which their naming rules require.
func jobName(id execution.ID) string {
	return namePrefix + id.String()
}

// isNotFound reports whether an error is Google saying the resource is gone.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "http 404")
}
