// -------------------------------------------------------------------------------
// Task to Invocation
//
// Author: Alex Freidah
//
// A function task names the deployed function in its config block and passes
// its args and env as the event. What the function does with them is between
// the operator and their function.
// -------------------------------------------------------------------------------

package aws

import (
	"encoding/json"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// ConfigFunction is the driver config key naming the function to invoke: a
// name, a partial ARN, or a full ARN, optionally with a version or alias.
const ConfigFunction = "function"

// event is the payload every invocation receives.
type event struct {
	ExecutionID string            `json:"execution_id"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

// invocation reads the function a task names and builds its event.
//
// A task naming no function is a caller error, not something to default:
// Vagabond will not guess what to run.
func invocation(id execution.ID, task *job.Task) (string, []byte, error) {
	function, ok := configValue(task, ConfigFunction, cty.String)
	if !ok || function.AsString() == "" {
		return "", nil, plugin.Internal(fmt.Errorf("task %q names no function", task.Name))
	}

	ev := event{ExecutionID: id.String()}

	if args, ok := configValue(task, "args", cty.List(cty.String)); ok {
		for it := args.ElementIterator(); it.Next(); {
			_, element := it.Element()
			ev.Args = append(ev.Args, element.AsString())
		}
	}

	env, diags := task.Environment()
	if diags.HasErrors() {
		return "", nil, plugin.Internal(fmt.Errorf("task %q env: %s", task.Name, diags.Error()))
	}

	ev.Env = env

	payload, err := json.Marshal(ev)
	if err != nil {
		return "", nil, plugin.Internal(fmt.Errorf("encoding the event: %w", err))
	}

	return function.AsString(), payload, nil
}

// configValue reads one attribute from the driver config as want.
//
// PartialContent, so a field meant for another driver is left alone.
func configValue(task *job.Task, name string, want cty.Type) (cty.Value, bool) {
	if task.Config == nil || task.Config.Body == nil {
		return cty.NilVal, false
	}

	content, _, _ := task.Config.Body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: name}},
	})

	attr, ok := content.Attributes[name]
	if !ok {
		return cty.NilVal, false
	}

	value, diags := attr.Expr.Value(task.Vars)
	if diags.HasErrors() || value.IsNull() || !value.IsKnown() {
		return cty.NilVal, false
	}

	converted, err := convert.Convert(value, want)
	if err != nil {
		return cty.NilVal, false
	}

	return converted, true
}
