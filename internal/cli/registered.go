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

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/registry"
)

// -------------------------------------------------------------------------
// INTERFACE
// -------------------------------------------------------------------------

// cliJobStore is what the registered-job commands read and write: jobs, their
// versions, and the executions they produced.
type cliJobStore interface {
	Register(ctx context.Context, namespace, name string, source []byte, now time.Time) (int64, bool, error)
	Job(ctx context.Context, namespace, name string) (*jobs.Job, error)
	Jobs(ctx context.Context, namespace string) ([]*jobs.Job, error)
	Version(ctx context.Context, namespace, name string, version int64) (*jobs.Version, error)
	Versions(ctx context.Context, namespace, name string) ([]*jobs.Version, error)
	Stop(ctx context.Context, namespace, name string, now time.Time) error
	JobExecutions(ctx context.Context, namespace, job string, limit int) ([]*execution.Record, error)
}

// -------------------------------------------------------------------------
// STORES
// -------------------------------------------------------------------------

// loadJobStores loads configuration and opens the store, refusing without a
// store block. The function returned closes the store.
func (m *Meta) loadJobStores(
	ctx context.Context, configPath, action string,
) (*registry.Registry, *stores, func(), int) {
	reg, cfg, code := m.loadRegistry(ctx, configPath)
	if reg == nil {
		return nil, nil, nil, code
	}

	store := cfg.Store
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

// -------------------------------------------------------------------------
// LOADING
// -------------------------------------------------------------------------

// readSource reads a job file's bytes as written, or standard input for "-",
// for commands that store the source rather than only parse it.
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

// loaded renders what loading a job reported, as job validate does, and hands
// back the job when nothing was wrong.
func (m *Meta) loaded(parsed *jobspec.Parsed, diags hcl.Diagnostics) (*job.File, int) {
	if diags.HasErrors() {
		renderDiagnostics(m.Ui, parsed.Files(), diags, m.color())

		return nil, ExitFailure
	}

	return parsed.Spec, ExitSuccess
}

// loadRegistered reads a registered job's current version and loads it for
// dispatch with meta. A stopped job is refused.
func (m *Meta) loadRegistered(
	ctx context.Context, s *stores, namespace, name string, meta metaFlags,
) (*job.File, *jobs.Version, int) {
	version, err := jobs.Current(ctx, s.jobs, namespace, name)
	if err != nil {
		return nil, nil, m.Errorf("%s", err)
	}

	parsed, diags, err := jobs.ForDispatch(fmt.Sprintf("%s (version %d)", name, version.Version), version.Source, meta)
	if err != nil {
		return nil, nil, m.Errorf("%s", err)
	}

	spec, code := m.loaded(parsed, diags)
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
