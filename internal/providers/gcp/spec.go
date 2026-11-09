// -------------------------------------------------------------------------------
// Task to Job Spec
//
// Author: Alex Freidah
//
// Turning a normalized task into what Cloud Run wants. Two settings matter
// most: retries, because Google's default would multiply a failing job behind
// the ledger's back, and resources, because Vagabond's units are not Cloud
// Run's.
// -------------------------------------------------------------------------------

package gcp

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// What Cloud Run accepts, published through Capabilities. CPU is in millicores
// to match job.Resources; the duration is Cloud Run's task timeout maximum.
const (
	maxCPU      = 8000
	maxMemory   = 32768
	maxDuration = 24 * time.Hour
)

// Defaults for a task that states nothing. One vCPU to 2 GiB is the ratio at
// which the free tier's vCPU-seconds and GiB-seconds run out together.
const (
	defaultCPU    = 1000
	defaultMemory = 2048
)

// -------------------------------------------------------------------------
// THE SPEC
// -------------------------------------------------------------------------

// jobSpec builds the Cloud Run job for a task.
func (p *Provider) jobSpec(task *job.Task) (map[string]any, error) {
	image, ok := task.ConfigString(job.ConfigImage)
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

	if command, ok := task.ConfigString("command"); ok {
		container["command"] = []string{command}
	}

	if args, ok := task.ConfigStrings("args"); ok {
		container["args"] = args
	}

	env, err := environment(task)
	if err != nil {
		return nil, err
	}

	if len(env) > 0 {
		container["env"] = env
	}

	return map[string]any{
		"template": map[string]any{
			"taskCount": 1,
			"template": map[string]any{
				"containers": []map[string]any{container},
				"timeout":    fmt.Sprintf("%ds", int(timeout.Seconds())),

				// The default of 3 bills four runs the ledger counts as one
				"maxRetries": 0,

				// An identity holding nothing, not Vagabond's own
				"serviceAccount": p.cfg.RuntimeServiceAccount,
			},
		},
	}, nil
}

// -------------------------------------------------------------------------
// RESOURCES
// -------------------------------------------------------------------------

// resourceLimits translates a task's requirements into Cloud Run's whole vCPU
// and memory string, rounding up: less than asked for fails mysteriously.
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

// vCPU rounds millicores up to the whole cores Cloud Run jobs accept, at least
// one, so 250 millicores is one core rather than none.
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

// environment renders a task's environment the way Cloud Run wants it: its env
// block plus the VAGABOND_META_* variables. One that does not evaluate is an
// admission failure, never a job run with no environment.
func environment(task *job.Task) ([]map[string]string, error) {
	attrs, diags := task.Environment()
	if diags.HasErrors() {
		return nil, plugin.Internal(fmt.Errorf("task %q env: %s", task.Name, diags.Error()))
	}

	env := make([]map[string]string, 0, len(attrs))
	for name, value := range attrs {
		env = append(env, map[string]string{"name": name, "value": value})
	}

	// Sorted, so two submissions of the same task send identical specs
	slices.SortFunc(env, func(a, b map[string]string) int { return strings.Compare(a["name"], b["name"]) })

	return env, nil
}
