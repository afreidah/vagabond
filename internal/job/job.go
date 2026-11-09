// -------------------------------------------------------------------------------
// Job - the top level of a Vagabond specification
//
// Author: Alex Freidah
//
// A job is a named collection of tasks plus the routing policy that applies to
// them, and the unit a client submits and admission reasons about. File holds
// more than one so the parser can report a second job in a file as a schema
// error rather than silently discarding it.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// DefaultNamespace is where a job naming no namespace runs. It exists without
// being declared.
const DefaultNamespace = "default"

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
// A job with no routing block is admissible everywhere, which differs from an
// empty provider list that allows no provider.
//
// Namespace is nil when the job names none; the caller's -namespace or the
// default namespace applies.
type Job struct {
	Name          string         `hcl:"name,label"`
	Namespace     *string        `hcl:"namespace,optional"`
	Type          *Type          `hcl:"type,optional"`
	Meta          *RawBlock      `hcl:"meta,block"`
	Parameterized *Parameterized `hcl:"parameterized,block"`
	Routing       *Routing       `hcl:"routing,block"`
	Tasks         []Task         `hcl:"task,block"`
}

// -------------------------------------------------------------------------
// JOB TYPE
// -------------------------------------------------------------------------

// Type is the scheduling model a job follows.
type Type string

// The job types Vagabond implements. TypeBatch runs each task to completion;
// long-running types are absent by design, since a service has no completion
// to report and no exit status to return.
const (
	TypeBatch Type = "batch"
)

var typeNames = []Type{TypeBatch}

// ErrUnknownType is the sentinel behind every unknown-job-type failure.
var ErrUnknownType = errors.New("unknown job type")

// Types returns the valid job types in declaration order, as a copy.
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

// unknownType builds the ErrUnknownType failure for name, listing valid ones.
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

// Parameterized declares the metadata a caller supplies at submission, which
// turns one job definition into a template run against many inputs.
//
// A missing MetaRequired key is a submission error caught before any provider
// is contacted. A dispatch supplying a key in neither list is refused.
type Parameterized struct {
	MetaRequired []string `hcl:"meta_required,optional"`
	MetaOptional []string `hcl:"meta_optional,optional"`
}
