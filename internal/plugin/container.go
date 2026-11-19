// -------------------------------------------------------------------------------
// Container Tasks - a container task as every container platform reads it
//
// Author: Alex Freidah
//
// Cloud Run, Code Engine and agent nodes all run a container task from the
// same few things: its image, command, arguments, environment, working
// directory, size and timeout. They are read here once, sized by the
// provider's published defaults so a task runs at the size admission checked,
// and each plugin turns the result into its own platform's form.
// -------------------------------------------------------------------------------

package plugin

import (
	"fmt"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ptr"
)

// Container is a container task as a platform runs it. An empty Command keeps
// the image's entrypoint, and Args follow it, or the image's own command when
// both are empty. CPU and Memory are what it declared, or the provider's
// defaults, and a zero Timeout is none set.
type Container struct {
	Image      string
	Command    []string
	Args       []string
	Env        map[string]string
	WorkingDir string
	CPU        int // millicores
	Memory     int // MiB
	Timeout    time.Duration
}

// ContainerOf reads a container task once, sized by defaults for what it does
// not declare. A task with no image, or an environment or timeout that does not
// evaluate, is an internal failure: the task's own, which no other provider
// would run either.
func ContainerOf(task *job.Task, defaults Resources) (Container, error) {
	image, ok := task.ConfigString(job.ConfigImage)
	if !ok {
		return Container{}, Internal(fmt.Errorf("task %q names no image", task.Name))
	}

	env, diags := task.Environment()
	if diags.HasErrors() {
		return Container{}, Internal(fmt.Errorf("task %q env: %s", task.Name, diags.Error()))
	}

	size := Declared(task).Or(defaults)
	out := Container{Image: image, Env: env, CPU: size.CPU, Memory: size.Memory}

	if command, ok := task.ConfigString("command"); ok {
		out.Command = []string{command}
	}

	if args, ok := task.ConfigStrings("args"); ok {
		out.Args = args
	}

	if task.WorkingDirectory != nil {
		out.WorkingDir = *task.WorkingDirectory
	}

	// None set stays zero, which each platform reads as its own limit or none.
	if task.Timeout != nil {
		timeout, err := task.Timeout.Std()
		if err != nil {
			return Container{}, Internal(fmt.Errorf("task %q timeout: %w", task.Name, err))
		}

		out.Timeout = timeout
	}

	return out, nil
}

// Declared is the size a task declared, zero for what it did not.
func Declared(task *job.Task) Resources {
	if task.Resources == nil {
		return Resources{}
	}

	return Resources{CPU: ptr.Deref(task.Resources.CPU), Memory: ptr.Deref(task.Resources.Memory)}
}
