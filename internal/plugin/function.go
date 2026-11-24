// -------------------------------------------------------------------------------
// Function Tasks - a function task as every function platform invokes it
//
// Author: Alex Freidah
//
// Lambda and OCI Functions invoke a deployed function the same way: the task's
// args and env go in as one event, and the function's reply comes back. Neither
// platform reports an exit code of its own, so the reply carries the task's
// exit code and output, and a reply that does not is the task failing.
// -------------------------------------------------------------------------------

package plugin

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// INVOCATION
// -------------------------------------------------------------------------

// ConfigFunction is the driver config key naming the function to invoke, in
// whatever form the platform identifies one.
const ConfigFunction = "function"

// Function is a function task as a platform invokes it: the function its
// config names and the event, encoded, that the function is sent.
type Function struct {
	Name  string
	Event []byte
}

// event is what every function receives. Env is data in the event, not the
// function's process environment, which is fixed where it is deployed.
type event struct {
	ExecutionID string            `json:"execution_id"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

// FunctionOf reads a function task once into the function it names and the
// event that function is sent. A task naming no function, or with an env that
// does not evaluate, is an internal failure no other provider would run.
func FunctionOf(id execution.ID, task *job.Task) (Function, error) {
	name, ok := task.ConfigString(ConfigFunction)
	if !ok || name == "" {
		return Function{}, Internal(fmt.Errorf("task %q names no function", task.Name))
	}

	env, err := environment(task)
	if err != nil {
		return Function{}, err
	}

	args, _ := task.ConfigStrings("args")

	// Strings and a map of them, which always encode
	encoded, _ := json.Marshal(event{ExecutionID: id.String(), Args: args, Env: env})

	return Function{Name: name, Event: encoded}, nil
}

// -------------------------------------------------------------------------
// REPLY
// -------------------------------------------------------------------------

// reply is what a function answers with. ExitCode is a pointer so a reply
// that leaves it out is told apart from one reporting zero.
type reply struct {
	ExitCode *int   `json:"exit_code"`
	Output   string `json:"output"`
}

// Replied is the submission of a function that answered with body after
// running for took. A body that is not a reply with an exit code fails the
// task with exit code 1, kept whole as its output, since a crashed or
// timed-out function answers with the platform's own error.
func Replied(id execution.ID, providerID string, body []byte, took time.Duration) Submission {
	result := &execution.Result{ID: id, ExitCode: new(1), Duration: took, Logs: body}

	var r reply
	if json.Unmarshal(body, &r) == nil && r.ExitCode != nil {
		result.ExitCode, result.Logs = r.ExitCode, []byte(r.Output)
	}

	state := execution.StateSucceeded
	if *result.ExitCode != 0 {
		state = execution.StateFailed
	}

	return Submission{ProviderID: providerID, State: state, Result: result}
}
