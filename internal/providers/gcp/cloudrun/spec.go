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

package cloudrun

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/providers/gcp"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// The largest task Cloud Run accepts, published through Capabilities. CPU is
// in millicores to match job.Resources.
const (
	maxCPU    = 8000
	maxMemory = 32768
)

// Defaults for a task that states nothing. One vCPU to 2 GiB is the ratio at
// which the free tier's vCPU-seconds and GiB-seconds run out together.
const (
	defaultCPU    = 1000
	defaultMemory = 2048
)

// platform is Cloud Run as Capabilities publishes it and jobSpec sizes a task.
// Jobs always run in the second generation execution environment, a microVM.
var platform = plugin.Cloud{
	Max:         plugin.Resources{CPU: maxCPU, Memory: maxMemory},
	Default:     plugin.Resources{CPU: defaultCPU, Memory: defaultMemory},
	MaxDuration: gcp.MaxDuration,
	Isolation:   plugin.IsolationVM,
}

// -------------------------------------------------------------------------
// THE SPEC
// -------------------------------------------------------------------------

// jobSpec builds the Cloud Run job for a task. One with no timeout runs to
// Cloud Run's maximum, not zero, which would end the job as it started.
func (p *Provider) jobSpec(task *job.Task) (map[string]any, error) {
	c, err := plugin.ContainerOf(task, platform.Default)
	if err != nil {
		return nil, err
	}

	container := map[string]any{
		"image":     c.Image,
		"resources": map[string]any{"limits": resourceLimits(c.CPU, c.Memory)},
	}

	if len(c.Command) > 0 {
		container["command"] = c.Command
	}

	if len(c.Args) > 0 {
		container["args"] = c.Args
	}

	if env := environment(c.Env); len(env) > 0 {
		container["env"] = env
	}

	if c.WorkingDir != "" {
		container["workingDir"] = c.WorkingDir
	}

	return map[string]any{
		"template": map[string]any{
			"taskCount": 1,
			"template": map[string]any{
				"containers": []map[string]any{container},
				"timeout":    gcp.Timeout(c.Timeout),

				// The default of 3 bills four runs the ledger counts as one
				"maxRetries": 0,

				// An identity holding nothing, not Vagabond's own
				"serviceAccount": p.Config.RuntimeServiceAccount,
			},
		},
	}, nil
}

// -------------------------------------------------------------------------
// RESOURCES
// -------------------------------------------------------------------------

// resourceLimits translates a size into Cloud Run's whole vCPU and memory
// string, rounding up: less than asked for fails mysteriously.
func resourceLimits(cpu, memory int) map[string]string {
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

// -------------------------------------------------------------------------
// THE DRIVER CONFIG
// -------------------------------------------------------------------------

// environment renders a task's environment the way Cloud Run wants it, sorted
// so two submissions of the same task send identical specs.
func environment(vars map[string]string) []map[string]string {
	env := make([]map[string]string, 0, len(vars))
	for name, value := range vars {
		env = append(env, map[string]string{"name": name, "value": value})
	}

	slices.SortFunc(env, func(a, b map[string]string) int { return strings.Compare(a["name"], b["name"]) })

	return env
}
