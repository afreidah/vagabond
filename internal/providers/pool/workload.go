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
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// defaultResources is what a task that declares no CPU or memory is sized at.
// Published in every node's capabilities, so admission checks the same size
// the workload is placed and run at.
var defaultResources = plugin.Resources{CPU: 1000, Memory: 1024}

// workloadOf translates a task into the workload an agent runs. A task with no
// image, or an unreadable environment or timeout, is an admission failure, not
// the node's. No timeout is no timeout: the agent only stops a workload that
// set one.
func workloadOf(task *job.Task) (*agentrpc.Workload, error) {
	c, err := plugin.ContainerOf(task, defaultResources)
	if err != nil {
		return nil, err
	}

	w := &agentrpc.Workload{
		Image:      c.Image,
		Command:    c.Command,
		Args:       c.Args,
		Env:        c.Env,
		WorkingDir: c.WorkingDir,
		Resources:  &agentrpc.Resources{Cpu: int64(c.CPU), Memory: int64(c.Memory)},
	}

	if c.Timeout > 0 {
		w.Timeout = durationpb.New(c.Timeout)
	}

	return w, nil
}

// resourcesOf is the size a task runs at on a node: what it declared, with
// defaults for what it did not.
func resourcesOf(task *job.Task) *agentrpc.Resources {
	size := plugin.Declared(task).Or(defaultResources)

	return &agentrpc.Resources{Cpu: int64(size.CPU), Memory: int64(size.Memory)}
}
