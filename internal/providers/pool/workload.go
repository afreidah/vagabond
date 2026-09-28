// -------------------------------------------------------------------------------
// Task to Workload
//
// Author: Alex Freidah
//
// What an agent is sent: the task's container, already decoded, so the agent
// only runs it. A task that declares no resources gets defaults, because a
// workload with no declared size would take room nothing accounts for.
// -------------------------------------------------------------------------------

package pool

import (
	"fmt"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// defaultResources is what a task that declares no CPU or memory is sized at.
// Published in the pool's capabilities, so admission checks the same size the
// workload is placed and run at.
var defaultResources = plugin.Resources{CPU: 1000, Memory: 1024}

// workloadOf translates a task into the workload an agent runs. A task with no
// image, or an unreadable timeout, is our failure to have admitted it, not the
// node's.
func workloadOf(task *job.Task) (*agentrpc.Workload, error) {
	image, ok := task.ConfigString(job.ConfigImage)
	if !ok {
		return nil, plugin.Internal(fmt.Errorf("task %q names no image", task.Name))
	}

	env, diags := task.Environment()
	if diags.HasErrors() {
		return nil, plugin.Internal(fmt.Errorf("task %q env: %s", task.Name, diags.Error()))
	}

	w := &agentrpc.Workload{Image: image, Env: env, Resources: resourcesOf(task)}

	if command, ok := task.ConfigString("command"); ok {
		w.Command = []string{command}
	}

	if args, ok := task.ConfigStrings("args"); ok {
		w.Args = args
	}

	if task.WorkingDirectory != nil {
		w.WorkingDir = *task.WorkingDirectory
	}

	// No timeout is no timeout: the agent only stops a workload that set one.
	if task.Timeout != nil {
		timeout, err := task.Timeout.Std()
		if err != nil {
			return nil, plugin.Internal(fmt.Errorf("task %q timeout: %w", task.Name, err))
		}

		w.Timeout = durationpb.New(timeout)
	}

	return w, nil
}

// resourcesOf is what the task declared, with defaults for what it did not.
func resourcesOf(task *job.Task) *agentrpc.Resources {
	out := &agentrpc.Resources{Cpu: int64(defaultResources.CPU), Memory: int64(defaultResources.Memory)}

	if r := task.Resources; r != nil {
		if r.CPU != nil && *r.CPU > 0 {
			out.Cpu = int64(*r.CPU)
		}

		if r.Memory != nil && *r.Memory > 0 {
			out.Memory = int64(*r.Memory)
		}
	}

	return out
}
