// -------------------------------------------------------------------------------
// Cloud Build Provider
//
// Author: Alex Freidah
//
// The Provider methods against Cloud Build, which runs a container task to
// completion as a build of one step, on the one machine its free minutes
// count. Google names the build, so each is tagged with its execution's
// resource name and found by that tag. Its output comes from Cloud Logging, as
// Cloud Run's does.
// -------------------------------------------------------------------------------

package cloudbuild

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/providers/gcp"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Type is what a provider block names to select Cloud Build.
const Type = "cloud-build"

// buildEndpoint is Cloud Build's API.
const buildEndpoint = "https://cloudbuild.googleapis.com"

// The machine every build runs on: e2-standard-2, the only one the free
// minutes count, with 2 vCPU and 8 GB. Builds may run for 24 hours.
const (
	buildMachine = "E2_STANDARD_2"
	buildCPU     = 2000
	buildMemory  = 8192
)

// platform is Cloud Build as Capabilities publishes it. Google does not say
// each build runs on a VM of its own, so only container isolation is claimed.
var platform = plugin.Cloud{
	Max:         plugin.Resources{CPU: buildCPU, Memory: buildMemory},
	Default:     plugin.Resources{CPU: buildCPU, Memory: buildMemory},
	MaxDuration: gcp.MaxDuration,
	Isolation:   plugin.IsolationContainer,
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Compile-time proof the contract is met.
var (
	_ plugin.Provider       = (*Provider)(nil)
	_ plugin.LogCapturer    = (*Provider)(nil)
	_ plugin.Recredentialer = (*Provider)(nil)
)

// Provider dispatches container tasks to Cloud Build. The endpoint is a field
// so a test can point the plugin at a local server.
type Provider struct {
	*gcp.API

	buildURL string
}

// New builds a Cloud Build provider from its configuration block, returning
// diagnostics so a bad field points at the line holding it.
func New(
	ctx context.Context, name string, body hcl.Body, credentials []byte,
) (*Provider, hcl.Diagnostics) {
	api, diags := gcp.NewAPI(ctx, name, Type, body, credentials)
	if diags.HasErrors() {
		return nil, diags
	}

	return &Provider{API: api, buildURL: buildEndpoint}, diags
}

// Capabilities reports the one machine every build runs on: constants, since
// nothing here varies by account.
func (p *Provider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.CloudContainer(platform), nil
}

// -------------------------------------------------------------------------
// SUBMIT
// -------------------------------------------------------------------------

// Submit starts a build of one step running the task. A build that runs as
// its own service account must log to Cloud Logging alone, which is where the
// output is read from anyway.
func (p *Provider) Submit(
	ctx context.Context, id execution.ID, task *job.Task,
) (plugin.Submission, error) {
	c, err := plugin.ContainerOf(task, platform.Default)
	if err != nil {
		return plugin.Submission{}, err
	}

	step := map[string]any{"name": c.Image}

	// A step takes one entrypoint, and the task's command is one word
	if len(c.Command) > 0 {
		step["entrypoint"] = c.Command[0]
	}

	// Cloud Build reads $NAME in args and env as one of its substitutions
	if len(c.Args) > 0 {
		step["args"] = literals(c.Args)
	}

	if env := stepEnv(c.Env); len(env) > 0 {
		step["env"] = literals(env)
	}

	if c.WorkingDir != "" {
		step["dir"] = c.WorkingDir
	}

	build := map[string]any{
		"steps":          []any{step},
		"timeout":        gcp.Timeout(c.Timeout),
		"tags":           []string{plugin.ResourceName(id)},
		"serviceAccount": "projects/" + p.Config.Project + "/serviceAccounts/" + p.Config.RuntimeServiceAccount,
		"options":        map[string]any{"machineType": buildMachine, "logging": "CLOUD_LOGGING_ONLY"},
	}

	var started struct {
		Metadata struct {
			Build struct {
				ID string `json:"id"`
			} `json:"build"`
		} `json:"metadata"`
	}

	if err := p.Call(ctx, http.MethodPost, p.buildsURL(), build, &started); err != nil {
		return plugin.Submission{}, err
	}

	return plugin.Submission{ProviderID: started.Metadata.Build.ID, State: execution.StateAccepted}, nil
}

// stepEnv renders a task's environment as a build step takes it, KEY=VALUE,
// sorted so two submissions of the same task send identical builds.
func stepEnv(vars map[string]string) []string {
	env := make([]string, 0, len(vars))
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		env = append(env, name+"="+vars[name])
	}

	return env
}

// literals doubles every $ in each value, which Cloud Build passes to the
// step as one $ instead of reading $NAME as a substitution of its own.
func literals(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.ReplaceAll(v, "$", "$$")
	}

	return out
}

// -------------------------------------------------------------------------
// THE BUILD
// -------------------------------------------------------------------------

// buildRun is the part of a build this plugin reads.
type buildRun struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	StatusDetail string `json:"statusDetail"`
	StartTime    string `json:"startTime"`
	FinishTime   string `json:"finishTime"`
	Steps        []struct {
		ExitCode *int `json:"exitCode"`
	} `json:"steps"`
}

// build finds an execution's build by the tag Submit gave it. None is
// plugin.ErrUnknownExecution, kept infrastructure since right after Submit
// the listing can lag the build.
func (p *Provider) build(ctx context.Context, id execution.ID) (*buildRun, error) {
	tag := plugin.ResourceName(id)
	query := url.Values{"filter": {fmt.Sprintf("tags=%q", tag)}}

	var out struct {
		Builds []buildRun `json:"builds"`
	}

	if err := p.Call(ctx, http.MethodGet, p.buildsURL()+"?"+query.Encode(), nil, &out); err != nil {
		return nil, err
	}

	if len(out.Builds) == 0 {
		return nil, plugin.Infrastructure(plugin.UnknownExecution("no build is tagged %s", tag))
	}

	return &out.Builds[0], nil
}

// state translates the build's status. A build that Google ended itself,
// failing inside or never leaving the queue, is failed here and explained by
// Result as infrastructure, so the task is tried elsewhere.
func (b *buildRun) state() (execution.State, error) {
	switch b.Status {
	case "PENDING", "QUEUED":
		return execution.StateAccepted, nil
	case "WORKING":
		return execution.StateRunning, nil
	case "SUCCESS":
		return execution.StateSucceeded, nil
	case "FAILURE", "TIMEOUT", "INTERNAL_ERROR", "EXPIRED":
		return execution.StateFailed, nil
	case "CANCELLED":
		return execution.StateCancelled, nil
	default:
		return "", plugin.Internal(fmt.Errorf("build %s has status %q, which this plugin does not know", b.ID, b.Status))
	}
}

// -------------------------------------------------------------------------
// STATUS AND RESULT
// -------------------------------------------------------------------------

// Status reports where an execution's build has reached.
func (p *Provider) Status(ctx context.Context, id execution.ID) (execution.Status, error) {
	b, err := p.build(ctx, id)
	if err != nil {
		return execution.Status{}, err
	}

	state, err := b.state()
	if err != nil {
		return execution.Status{}, err
	}

	return plugin.ObservedStatus(id, state, b.ID, plugin.ParseTime(b.StartTime), plugin.ParseTime(b.FinishTime)), nil
}

// Result returns the step's exit code and how long the build ran, which is
// what the free minutes count. A build Google ended itself is infrastructure.
// The output is read apart, by CaptureLogs.
func (p *Provider) Result(ctx context.Context, id execution.ID) (*execution.Result, error) {
	b, err := p.build(ctx, id)
	if err != nil {
		return nil, err
	}

	if b.Status == "INTERNAL_ERROR" || b.Status == "EXPIRED" {
		return nil, plugin.Infrastructure(fmt.Errorf("build %s ended %s: %s", b.ID, b.Status, b.StatusDetail))
	}

	result := &execution.Result{
		ID:       id,
		Duration: execution.Elapsed(plugin.ParseTime(b.StartTime), plugin.ParseTime(b.FinishTime)),
	}

	if len(b.Steps) > 0 {
		result.ExitCode = b.Steps[0].ExitCode
	}

	// Past its timeout, the platform ended the task rather than the task itself
	if b.Status == "TIMEOUT" {
		result.Reason = b.StatusDetail
	}

	return result, nil
}

// -------------------------------------------------------------------------
// OUTPUT
// -------------------------------------------------------------------------

// CaptureLogs copies what a build's step prints into log as it is logged. A
// cancelled build keeps its output, so nothing this reads is destroyed.
func (p *Provider) CaptureLogs(ctx context.Context, id execution.ID, log execution.Log, ended <-chan struct{}) error {
	filter, err := p.buildLogs(ctx, id)
	if err != nil {
		return err
	}

	return p.CaptureEntries(ctx, filter, log, ended)
}

// buildLogs is the filter picking out the output of an execution's build.
func (p *Provider) buildLogs(ctx context.Context, id execution.ID) (string, error) {
	b, err := p.build(ctx, id)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(`resource.type="build" resource.labels.build_id=%q`, b.ID), nil
}

// -------------------------------------------------------------------------
// CANCEL
// -------------------------------------------------------------------------

// Cancel stops an execution's build. A build no listing shows has nothing to
// stop, and a cancelled build keeps its record and output, so nothing else is
// released.
func (p *Provider) Cancel(ctx context.Context, id execution.ID) error {
	b, err := p.build(ctx, id)
	if errors.Is(err, plugin.ErrUnknownExecution) {
		return nil
	}

	if err != nil {
		return err
	}

	cancel := map[string]string{"projectId": p.Config.Project, "id": b.ID}

	return plugin.IgnoreNotFound(p.Call(ctx, http.MethodPost, p.buildsURL()+"/"+b.ID+":cancel", cancel, nil))
}

// -------------------------------------------------------------------------
// ENDPOINTS
// -------------------------------------------------------------------------

// buildsURL is the collection every build of this provider lives in, under a
// base the provider holds so a test can point it at a local server.
func (p *Provider) buildsURL() string {
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/builds", p.buildURL, p.Config.Project, p.Config.Region)
}
