// -------------------------------------------------------------------------------
// Task - the unit of work a job dispatches
//
// Author: Alex Freidah
//
// A task states what it needs rather than who should run it: an execution
// contract, resource requirements, a source to fetch, and the limits it expects
// to run within. Nothing here names a provider, and a field that did would
// defeat the separation the whole architecture rests on.
//
// Optional scalars are pointers, following Nomad's api package. A value type
// cannot distinguish a field the author set to its zero value from one they
// omitted, and the two mean opposite things here: attempts = 0 forbids retrying
// while an absent attempts takes the default, and internet = false denies
// egress while an absent internet leaves the decision to the provider.
// -------------------------------------------------------------------------------

package job

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// -------------------------------------------------------------------------
// TASK
// -------------------------------------------------------------------------

// Task is one unit of work within a job.
//
// Timeout is the task's own bound, not a provider's. Admission compares it
// against each candidate's limit, and a task killed by a provider limit below
// its declared timeout is an admission bug rather than a workload failure.
type Task struct {
	Name             string                 `hcl:"name,label"`
	Driver           DriverName             `hcl:"driver"`
	Config           *RawBlock              `hcl:"config,block"`
	Env              *RawBlock              `hcl:"env,block"`
	Source           *Source                `hcl:"source,block"`
	WorkingDirectory *string                `hcl:"working_directory,optional"`
	Resources        *Resources             `hcl:"resources,block"`
	Timeout          *Duration              `hcl:"timeout,optional"`
	Network          *Network               `hcl:"network,block"`
	Execution        *ExecutionRequirements `hcl:"execution,block"`
	Retry            *Retry                 `hcl:"retry,block"`
}

// ConfigImage is the driver config key naming a container image.
//
// The config block's shape belongs to the driver, with one exception: whether a
// task names an image is something admission has to know, because a provider
// can run containers without running anyone's container. Lambda is the case
// that makes this real, and no capability comparison reaches it without reading
// this key.
const ConfigImage = "image"

// Image returns the container image this task names, if it names one.
//
// Reads that one attribute rather than decoding the block, because the rest of
// a driver config is not Vagabond's to understand: a command's args are a list
// and a driver may accept nested blocks, neither of which survives being
// flattened into a string map. Asking for one attribute leaves the rest for the
// driver.
//
// Evaluated against ctx because the config block is left undecoded at parse
// time and its values may still reference job metadata, as in an image tagged
// with the commit a CI system supplied.
func (t *Task) Image(ctx *hcl.EvalContext) (string, hcl.Diagnostics) {
	if t.Config == nil || t.Config.Body == nil {
		return "", nil
	}

	content, _, diags := t.Config.Body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: ConfigImage}},
	})

	attr, ok := content.Attributes[ConfigImage]
	if !ok {
		return "", diags
	}

	value, valueDiags := attr.Expr.Value(ctx)
	diags = append(diags, valueDiags...)

	if value.IsNull() || !value.IsKnown() {
		return "", diags
	}

	str, err := convert.Convert(value, cty.String)
	if err != nil {
		return "", append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Invalid image",
			Detail:   fmt.Sprintf("The %s must be a string: %s.", ConfigImage, err),
			Subject:  attr.Expr.Range().Ptr(),
		})
	}

	return str.AsString(), diags
}

// -------------------------------------------------------------------------
// UNDECODED CONFIG
// -------------------------------------------------------------------------

// RawBlock is a block whose attribute names are not part of Vagabond's schema.
//
// Three blocks qualify, for two different reasons. The task config block is
// genuinely dynamic: its shape depends on the driver, so only that driver can
// decode it, and nested blocks inside it are permitted and arbitrary. The meta
// and env blocks have a known value type, but their keys are chosen by the job
// author, so no schema can list them either.
//
// All three are bodies rather than maps because gohcl decodes a block into a
// struct and panics on a map field. Nomad's api package does tag these as
// map[string]string, but Nomad decodes them with its own decoder rather than
// gohcl, so the tag reads as advice that does not transfer.
//
// The body is the better representation regardless. Keeping it undecoded
// preserves HCL source ranges, which is what lets an error about an env value
// point at the line the author wrote rather than at a string that has already
// lost its origin.
type RawBlock struct {
	Body hcl.Body `hcl:",remain"`
}

// Attributes decodes the block into a string map, evaluating each value against
// ctx.
//
// Returns diagnostics rather than an error so that a bad value is reported
// against its own source range, and so that several bad values are reported
// together rather than one per run.
//
// A nil receiver yields nothing, because an absent block and an empty one carry
// the same meaning for metadata and environment.
func (b *RawBlock) Attributes(ctx *hcl.EvalContext) (map[string]string, hcl.Diagnostics) {
	if b == nil || b.Body == nil {
		return nil, nil
	}

	attrs, diags := b.Body.JustAttributes()
	if attrs == nil {
		return nil, diags
	}

	out := make(map[string]string, len(attrs))

	for name, attr := range attrs {
		value, valueDiags := attr.Expr.Value(ctx)
		diags = append(diags, valueDiags...)

		if value.IsNull() || !value.IsKnown() {
			continue
		}

		str, err := convert.Convert(value, cty.String)
		if err != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid value",
				Detail: fmt.Sprintf(
					"The value for %q must be a string: %s.", name, err),
				Subject: attr.Expr.Range().Ptr(),
			})

			continue
		}

		out[name] = str.AsString()
	}

	return out, diags
}

// -------------------------------------------------------------------------
// TASK BLOCKS
// -------------------------------------------------------------------------

// Source is the material a task operates on, fetched before execution begins.
//
// Ref is frequently a reference to job metadata supplied at submission rather
// than a literal, which is what makes one job definition usable across every
// commit a CI system wants verified.
type Source struct {
	Type        string  `hcl:"type"`
	Repository  string  `hcl:"repository"`
	Ref         *string `hcl:"ref,optional"`
	Destination *string `hcl:"destination,optional"`
}

// Resources is what a task requires, expressed as a workload requirement rather
// than any provider's sizing model.
//
// CPU is in MHz and Memory in MiB, matching the units a Nomad user already
// expects. Each provider plugin translates these into the closest configuration
// its platform offers, which is rarely an exact match and is the plugin's
// problem rather than the job author's.
type Resources struct {
	CPU    *int `hcl:"cpu,optional"`
	Memory *int `hcl:"memory,optional"`
}

// Network is the connectivity a task expects.
//
// Private is declared even though no provider satisfies it in phase 1. A job
// that needs private network access should be rejected by admission with a
// reason that names the problem, which requires the job to be able to ask.
type Network struct {
	Internet *bool `hcl:"internet,optional"`
	Private  *bool `hcl:"private,optional"`
}

// ExecutionRequirements is the environment a task must run in.
//
// Named for the requirement rather than for execution itself, because an
// Execution is a dispatched run of a task and the two would otherwise collide
// in every file that handles both.
type ExecutionRequirements struct {
	Architecture *Arch `hcl:"architecture,optional"`
	Privileged   *bool `hcl:"privileged,optional"`
}

// -------------------------------------------------------------------------
// RETRY
// -------------------------------------------------------------------------

// Retry is what to do when a task does not complete.
//
// Reroute distinguishes the two failures that look alike from a distance. An
// infrastructure failure means no verdict was reached and another admitted
// provider may produce one; a workload failure is a real answer and is returned
// unchanged however many attempts remain. Retrying a failing build across three
// providers burns free-tier capacity to reach the same result three times.
type Retry struct {
	Attempts *int     `hcl:"attempts,optional"`
	Reroute  *bool    `hcl:"reroute,optional"`
	Backoff  *Backoff `hcl:"backoff,block"`
}

// Backoff is the delay between retry attempts.
type Backoff struct {
	Initial *Duration `hcl:"initial,optional"`
	Max     *Duration `hcl:"max,optional"`
}
