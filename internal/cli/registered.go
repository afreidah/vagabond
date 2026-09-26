// -------------------------------------------------------------------------------
// Registered Jobs
//
// Author: Alex Freidah
//
// Shared by register, dispatch, status, stop, and plan by name. Every one of
// them needs a store block: a registered job has to outlive the process that
// registered it.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/registry"
)

// jobStore is what the registered-job commands read and write.
type jobStore interface {
	Register(ctx context.Context, namespace, name string, source []byte, now time.Time) (int64, bool, error)
	Job(ctx context.Context, namespace, name string) (*jobs.Job, error)
	Jobs(ctx context.Context, namespace string) ([]*jobs.Job, error)
	Version(ctx context.Context, namespace, name string, version int64) (*jobs.Version, error)
	Versions(ctx context.Context, namespace, name string) ([]*jobs.Version, error)
	Stop(ctx context.Context, namespace, name string, now time.Time) error
	JobExecutions(ctx context.Context, namespace, job string, limit int) ([]*execution.Record, error)
}

// loadJobStores loads configuration and opens the store, refusing without a
// store block. The function returned closes the store.
func (m *Meta) loadJobStores(
	ctx context.Context, configPath, action string,
) (*registry.Registry, *stores, func(), int) {
	reg, store, code := m.loadRegistry(ctx, configPath)
	if reg == nil {
		return nil, nil, nil, code
	}

	if store == nil {
		return nil, nil, nil, m.Errorf(
			"%s needs a store block: a registered job is kept in the database.", action)
	}

	// Opened directly rather than through loadStores, which offers -untracked:
	// a registered job cannot be read from anywhere but the store.
	s, finish, err := openStores(ctx, store.DSN, reg)
	if err != nil {
		return nil, nil, nil, m.Errorf("Could not open the store: %s", err)
	}

	return reg, s, finish, ExitSuccess
}

// readSource reads a job file, or standard input for "-".
func (m *Meta) readSource(path string) ([]byte, error) {
	if path == stdinPath {
		src, err := io.ReadAll(m.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading standard input: %w", err)
		}

		return src, nil
	}

	src, err := os.ReadFile(path) //nolint:gosec // the operator named this file
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	return src, nil
}

// loadSource parses and validates source with meta, reporting as job validate
// does.
func (m *Meta) loadSource(filename string, src []byte, meta map[string]string) (*job.File, int) {
	parsed, diags := jobspec.Parse(jobspec.Config{Filename: filename, Source: src, Meta: meta})

	if !diags.HasErrors() {
		diags = append(diags, jobspec.Validate(parsed.Spec)...)
	}

	if diags.HasErrors() {
		renderDiagnostics(m.Ui, parsed.Files(), diags, m.color())

		return nil, ExitFailure
	}

	return parsed.Spec, ExitSuccess
}

// loadRegistered reads a registered job's current version and parses it with
// meta, enforcing what a dispatch may supply first. A stopped job is refused.
func (m *Meta) loadRegistered(
	ctx context.Context, s *stores, namespace, name string, meta metaFlags,
) (*job.File, *jobs.Version, int) {
	current, err := s.jobs.Job(ctx, namespace, name)
	if err != nil {
		return nil, nil, m.Errorf("%s", err)
	}

	if current.Stopped {
		return nil, nil, m.Errorf("Job %q is stopped. Register it again to dispatch it.", name)
	}

	version, err := s.jobs.Version(ctx, namespace, name, current.Version)
	if err != nil {
		return nil, nil, m.Errorf("%s", err)
	}

	decl, diags := jobspec.Declared(name, version.Source)
	if diags.HasErrors() {
		return nil, nil, m.Errorf("Job %q version %d: %s", name, version.Version, diags.Error())
	}

	if err := decl.CheckDispatch(meta); err != nil {
		return nil, nil, m.Errorf("%s", err)
	}

	spec, code := m.loadSource(fmt.Sprintf("%s (version %d)", name, version.Version), version.Source, meta)
	if spec == nil {
		return nil, nil, code
	}

	return spec, version, ExitSuccess
}

// namespaceOf resolves the namespace for a command naming a registered job:
// the flag, else default. A job's own namespace is where it was registered.
func namespaceOf(flag string, reg *registry.Registry) (string, error) {
	return resolveNamespace(flag, &job.Job{}, reg)
}
