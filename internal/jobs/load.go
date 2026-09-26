// -------------------------------------------------------------------------------
// Loading Jobs
//
// Author: Alex Freidah
//
// Parsing and validating a job for each thing that can happen to it, and
// resolving the namespace it runs in. Diagnostics come back unrendered; each
// caller reports them in its own form.
// -------------------------------------------------------------------------------

package jobs

import (
	"context"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobspec"
)

// -------------------------------------------------------------------------
// INTERFACE
// -------------------------------------------------------------------------

// Reader reads registered jobs, which is all Current needs of a store.
type Reader interface {
	Job(ctx context.Context, namespace, name string) (*Job, error)
	Version(ctx context.Context, namespace, name string, version int64) (*Version, error)
}

// -------------------------------------------------------------------------
// LOADING
// -------------------------------------------------------------------------

// Load parses and validates source with meta, for a job about to run or be
// planned.
func Load(filename string, source []byte, meta map[string]string) (*jobspec.Parsed, hcl.Diagnostics) {
	parsed, diags := jobspec.Parse(jobspec.Config{Filename: filename, Source: source, Meta: meta})

	if !diags.HasErrors() {
		diags = append(diags, jobspec.Validate(parsed.Spec)...)
	}

	return parsed, diags
}

// ForRegister validates source in full, with each declared ${meta.key}
// standing as its own text, since the values arrive at dispatch.
func ForRegister(filename string, source []byte) (*jobspec.Parsed, hcl.Diagnostics) {
	decl, diags := jobspec.Declared(filename, source)
	if diags.HasErrors() {
		return nil, diags
	}

	return Load(filename, source, decl.References())
}

// ForDispatch refuses metadata the job does not permit, then loads it with the
// real values. A refusal is an error; a job that fails to load is diagnostics.
func ForDispatch(
	filename string, source []byte, meta map[string]string,
) (*jobspec.Parsed, hcl.Diagnostics, error) {
	decl, diags := jobspec.Declared(filename, source)
	if diags.HasErrors() {
		return nil, diags, nil
	}

	if err := decl.CheckDispatch(meta); err != nil {
		return nil, nil, err
	}

	parsed, diags := Load(filename, source, meta)

	return parsed, diags, nil
}

// Current returns a registered job's current version, refusing a stopped job
// with ErrStopped.
func Current(ctx context.Context, r Reader, namespace, name string) (*Version, error) {
	j, err := r.Job(ctx, namespace, name)
	if err != nil {
		return nil, err
	}

	if j.Stopped {
		return nil, fmt.Errorf("%w: %q; register it again to dispatch it", ErrStopped, name)
	}

	return r.Version(ctx, namespace, name, j.Version)
}

// -------------------------------------------------------------------------
// NAMESPACES
// -------------------------------------------------------------------------

// Namespace returns the namespace a job runs in: its own, else requested, else
// the default. A conflict between the two, or an undeclared namespace, is an
// error.
func Namespace(requested string, j *job.Job, declared func(string) bool) (string, error) {
	namespace := requested

	if j.Namespace != nil {
		if requested != "" && requested != *j.Namespace {
			return "", fmt.Errorf("job %q names namespace %q, but %q was requested; remove one",
				j.Name, *j.Namespace, requested)
		}

		namespace = *j.Namespace
	}

	if namespace == "" {
		namespace = job.DefaultNamespace
	}

	if !declared(namespace) {
		return "", fmt.Errorf("namespace %q is not declared in the configuration", namespace)
	}

	return namespace, nil
}
