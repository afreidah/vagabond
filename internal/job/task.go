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

import "github.com/hashicorp/hcl/v2"

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
	Env              map[string]string      `hcl:"env,optional"`
	Source           *Source                `hcl:"source,block"`
	WorkingDirectory *string                `hcl:"working_directory,optional"`
	Resources        *Resources             `hcl:"resources,block"`
	Timeout          *Duration              `hcl:"timeout,optional"`
	Network          *Network               `hcl:"network,block"`
	Execution        *ExecutionRequirements `hcl:"execution,block"`
	Retry            *Retry                 `hcl:"retry,block"`
}

// -------------------------------------------------------------------------
// UNDECODED CONFIG
// -------------------------------------------------------------------------

// RawBlock is a block whose schema Vagabond cannot know.
//
// Only the task config block qualifies. Its shape depends on the task's driver,
// so only that driver can decode it, and decoding it here would mean teaching
// this package every driver's configuration. Nested blocks inside it are
// permitted and arbitrary, which is why it cannot be a map the way meta and env
// can.
//
// Keeping the body undecoded preserves HCL source ranges, which is what lets a
// later decode error point at the line the author wrote rather than at a value
// that has already lost its origin.
type RawBlock struct {
	Body hcl.Body `hcl:",remain"`
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
