// -------------------------------------------------------------------------------
// Reading the Driver Config
//
// Author: Alex Freidah
//
// A task's config block is undecoded HCL whose shape belongs to the driver, so
// this plugin asks for the attributes it understands and leaves the rest. A
// schema naming everything would refuse a field some other container provider
// supports.
// -------------------------------------------------------------------------------

package gcp

import (
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// ATTRIBUTES
// -------------------------------------------------------------------------

// containerImage returns the image a task names.
//
// Absence is a caller error rather than something to default: Vagabond will
// not guess what to run, and admission already rejected a task with no image
// against a provider that needs one.
func containerImage(task *job.Task) (string, bool) {
	return containerString(task, job.ConfigImage)
}

// containerString reads one string attribute from the driver config.
func containerString(task *job.Task, name string) (string, bool) {
	attr, ok := configAttribute(task, name)
	if !ok {
		return "", false
	}

	value, diags := attr.Expr.Value(task.Vars)
	if diags.HasErrors() || value.IsNull() || !value.IsKnown() {
		return "", false
	}

	converted, err := convert.Convert(value, cty.String)
	if err != nil {
		return "", false
	}

	return converted.AsString(), true
}

// containerArgs reads the argument list a task passes to its command.
//
// A list rather than a string, which is the one place a driver config is not
// flat. Shell-splitting a string instead would make a path with a space in it
// two arguments.
func containerArgs(task *job.Task) ([]string, bool) {
	attr, ok := configAttribute(task, "args")
	if !ok {
		return nil, false
	}

	value, diags := attr.Expr.Value(task.Vars)
	if diags.HasErrors() || value.IsNull() || !value.IsKnown() {
		return nil, false
	}

	converted, err := convert.Convert(value, cty.List(cty.String))
	if err != nil {
		return nil, false
	}

	var args []string

	for it := converted.ElementIterator(); it.Next(); {
		_, element := it.Element()
		args = append(args, element.AsString())
	}

	return args, len(args) > 0
}

// configAttribute pulls one attribute out of the task's driver config.
//
// PartialContent rather than JustAttributes, so a config holding a nested
// block or a field meant for another driver is read without complaint.
func configAttribute(task *job.Task, name string) (*hcl.Attribute, bool) {
	if task.Config == nil || task.Config.Body == nil {
		return nil, false
	}

	content, _, _ := task.Config.Body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: name}},
	})

	attr, ok := content.Attributes[name]

	return attr, ok
}

// sortEnv orders environment variables by name.
func sortEnv(env []map[string]string) {
	sort.Slice(env, func(a, b int) bool {
		return env[a]["name"] < env[b]["name"]
	})
}
