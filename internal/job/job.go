// -------------------------------------------------------------------------------
// Job - the top level of a Vagabond specification
//
// Author: Alex Freidah
//
// A job is a named collection of tasks plus the routing policy that applies to
// them. It is the unit a client submits and the unit admission reasons about.
//
// The File type above it exists because a .vagabond.hcl file is a container
// rather than a job: the schema allows more than one, so that the parser can
// report a second job in a file as a schema error rather than silently
// discarding it.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// -------------------------------------------------------------------------
// FILE
// -------------------------------------------------------------------------

// File is the decoded contents of one .vagabond.hcl file.
type File struct {
	Jobs []Job `hcl:"job,block"`
}

// -------------------------------------------------------------------------
// JOB
// -------------------------------------------------------------------------

// Job is a named collection of tasks and the routing policy applied to them.
//
// Routing is optional because a job that states no preference is admissible
// everywhere, which is the sensible default for work that genuinely does not
// care where it runs. Its absence is not the same as an empty provider list,
// which would mean no provider is allowed.
type Job struct {
	Name          string            `hcl:"name,label"`
	Type          *Type             `hcl:"type,optional"`
	Meta          map[string]string `hcl:"meta,optional"`
	Parameterized *Parameterized    `hcl:"parameterized,block"`
	Routing       *Routing          `hcl:"routing,block"`
	Tasks         []Task            `hcl:"task,block"`
}

// -------------------------------------------------------------------------
// JOB TYPE
// -------------------------------------------------------------------------

// Type is the scheduling model a job follows.
type Type string

// The job types Vagabond implements.
//
// TypeBatch runs each task to completion and is the only type in phase 1.
// Vagabond brokers short-lived stateless work, so the long-running types a
// Nomad user would expect alongside it are deliberately absent rather than
// unimplemented: a service has no completion to report and no exit status to
// return.
const (
	TypeBatch Type = "batch"
)

var typeNames = []Type{TypeBatch}

// ErrUnknownType is the sentinel behind every unknown-job-type failure.
var ErrUnknownType = errors.New("unknown job type")

// Types returns the valid job types in declaration order.
//
// The returned slice is a copy, so a caller rendering it into an error cannot
// reorder the vocabulary for everyone else.
func Types() []Type {
	return slices.Clone(typeNames)
}

// Valid reports whether t is a job type Vagabond implements.
func (t Type) Valid() bool {
	return slices.Contains(typeNames, t)
}

// String returns the job type as written in a job file.
func (t Type) String() string {
	return string(t)
}

// ParseType converts s into a Type, or reports that it is not one.
func ParseType(s string) (Type, error) {
	t := Type(s)
	if !t.Valid() {
		return "", unknownType(s)
	}

	return t, nil
}

// MarshalText implements encoding.TextMarshaler.
func (t Type) MarshalText() ([]byte, error) {
	if !t.Valid() {
		return nil, unknownType(string(t))
	}

	return []byte(t), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *Type) UnmarshalText(text []byte) error {
	parsed, err := ParseType(string(text))
	if err != nil {
		return err
	}

	*t = parsed

	return nil
}

func unknownType(name string) error {
	valid := make([]string, 0, len(typeNames))
	for _, t := range typeNames {
		valid = append(valid, string(t))
	}

	return fmt.Errorf("%w %q: valid job types are %s",
		ErrUnknownType, name, strings.Join(valid, ", "))
}

// -------------------------------------------------------------------------
// PARAMETERIZED JOBS
// -------------------------------------------------------------------------

// Parameterized declares the metadata a caller must supply at submission.
//
// MetaRequired is what turns one job definition into a template usable across
// every commit a CI system wants verified: the definition names git_ref as
// required, and the caller supplies the value per submission. A missing
// required key is a submission error, caught before any provider is contacted
// and any free-tier capacity is spent.
type Parameterized struct {
	MetaRequired []string `hcl:"meta_required,optional"`
}
