// -------------------------------------------------------------------------------
// Task to Job Spec
//
// Author: Alex Freidah
//
// Turning a normalized task into what Cloud Run wants. Two settings here are
// worth more attention than the rest: retries, because Google's default would
// multiply a failing job behind the ledger's back, and resources, because
// Vagabond's units are not Cloud Run's.
// -------------------------------------------------------------------------------

package gcp

import (
	"fmt"
	"math"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// What Cloud Run will accept, published through Capabilities.
//
// The CPU ceiling is in millicores to match job.Resources, so eight vCPU is
// 8000. The duration is Cloud Run's own task timeout maximum.
const (
	maxCPU      = 8000
	maxMemory   = 32768
	maxDuration = 24 * time.Hour
)

// Defaults for a task that states nothing.
//
// One vCPU against two gibibytes is not arbitrary: it is the ratio at which
// the free tier's two dimensions, 240,000 vCPU-seconds and 450,000
// GiB-seconds, exhaust at roughly the same moment. Asking for more memory per
// core spends the memory allowance first and wastes the rest.
const (
	defaultCPU    = 1000
	defaultMemory = 2048
)

// -------------------------------------------------------------------------
// THE SPEC
// -------------------------------------------------------------------------

// jobSpec builds the Cloud Run job for a task.
func (p *Provider) jobSpec(task *job.Task) (map[string]any, error) {
	image, ok := containerImage(task)
	if !ok {
		return nil, plugin.Internal(
			fmt.Errorf("task %q names no image", task.Name))
	}

	timeout, err := taskTimeout(task)
	if err != nil {
		return nil, plugin.Internal(err)
	}

	container := map[string]any{
		"image":     image,
		"resources": map[string]any{"limits": resourceLimits(task)},
	}

	if command, ok := containerString(task, "command"); ok {
		container["command"] = []string{command}
	}

	if args, ok := containerArgs(task); ok {
		container["args"] = args
	}

	if env := environment(task); len(env) > 0 {
		container["env"] = env
	}

	return map[string]any{
		"template": map[string]any{
			"taskCount": 1,
			"template": map[string]any{
				"containers": []map[string]any{container},
				"timeout":    fmt.Sprintf("%ds", int(timeout.Seconds())),

				// Explicitly zero. Cloud Run's default is 3, so a failing job
				// would run four times and charge four times while the ledger
				// recorded one execution. Vagabond does its own retries, with
				// its own accounting, or it does not retry.
				"maxRetries": 0,

				// The container runs as an identity holding nothing, not as
				// the one Vagabond authenticates with. A compromised
				// dependency then reaches nothing.
				"serviceAccount": p.cfg.RuntimeServiceAccount,
			},
		},
	}, nil
}

// -------------------------------------------------------------------------
// RESOURCES
// -------------------------------------------------------------------------

// resourceLimits translates a task's requirements into Cloud Run's units.
//
// Vagabond states CPU in millicores and memory in MiB, which are workload
// requirements rather than any platform's sizing. Cloud Run wants whole vCPU
// for jobs and a memory string, so a request is rounded up: giving a task less
// than it asked for is the one translation error that produces a mysterious
// failure rather than a bill.
func resourceLimits(task *job.Task) map[string]string {
	cpu, memory := defaultCPU, defaultMemory

	if task.Resources != nil {
		if task.Resources.CPU != nil && *task.Resources.CPU > 0 {
			cpu = *task.Resources.CPU
		}

		if task.Resources.Memory != nil && *task.Resources.Memory > 0 {
			memory = *task.Resources.Memory
		}
	}

	return map[string]string{
		"cpu":    fmt.Sprintf("%d", vCPU(cpu)),
		"memory": fmt.Sprintf("%dMi", memory),
	}
}

// vCPU rounds millicores up to the whole cores Cloud Run jobs accept.
//
// At least one, because a job cannot be given a fraction of a core, and a task
// asking for 250 millicores wants a quarter of a core rather than none.
func vCPU(millicores int) int {
	cores := int(math.Ceil(float64(millicores) / 1000))
	if cores < 1 {
		return 1
	}

	return cores
}

// taskTimeout returns what the task declared, or Cloud Run's own default.
func taskTimeout(task *job.Task) (time.Duration, error) {
	if task.Timeout == nil {
		return maxDuration, nil
	}

	timeout, err := task.Timeout.Std()
	if err != nil {
		return 0, fmt.Errorf("task %q timeout: %w", task.Name, err)
	}

	return timeout, nil
}

// -------------------------------------------------------------------------
// THE DRIVER CONFIG
// -------------------------------------------------------------------------

// environment renders a task's env block the way Cloud Run wants it.
//
// Evaluated against no context because job metadata was already substituted
// when the specification was parsed; anything still unresolved here is a bug
// upstream rather than something to interpolate now.
func environment(task *job.Task) []map[string]string {
	attrs, diags := task.Env.Attributes(nil)
	if diags.HasErrors() {
		return nil
	}

	env := make([]map[string]string, 0, len(attrs))
	for name, value := range attrs {
		env = append(env, map[string]string{"name": name, "value": value})
	}

	// Sorted, because a map's order would make two submissions of the same
	// task differ and a diff of what was sent meaningless.
	sortEnv(env)

	return env
}
