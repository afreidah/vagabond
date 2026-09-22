// -------------------------------------------------------------------------------
// Loading a Job and its Providers
//
// Author: Alex Freidah
//
// Shared by plan and run, so that a bad job file or an unreachable provider
// reads identically whichever command hit it.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/registry"
)

// loadJob parses and validates the specification, reporting as job validate
// does so that the same mistake reads the same way in every command.
func (m *Meta) loadJob(path string, meta metaFlags) (*job.File, int) {
	parsed, diags := m.parseJob(path, meta)

	if !diags.HasErrors() {
		diags = append(diags, jobspec.Validate(parsed.Spec)...)
	}

	if diags.HasErrors() {
		renderDiagnostics(m.Ui, parsed.Files(), diags, m.color())

		return nil, ExitFailure
	}

	return parsed.Spec, ExitSuccess
}

// loadRegistry finds configuration, builds the providers, and refreshes them.
//
// A refresh failure is reported but does not stop the command. A provider that
// did not answer is marked unhealthy and rejected by name, which is more useful
// than refusing to proceed at all: the other providers still have answers.
func (m *Meta) loadRegistry(ctx context.Context, configPath string) (*registry.Registry, int) {
	path, err := config.Discover(configPath)
	if err != nil {
		return nil, m.Errorf("%s", err)
	}

	cfg, diags := config.LoadPath(path)
	if diags.HasErrors() {
		renderDiagnostics(m.Ui, nil, diags, m.color())

		return nil, ExitFailure
	}

	reg, diags := registry.New(ctx, cfg)
	if diags.HasErrors() {
		renderDiagnostics(m.Ui, nil, diags, m.color())

		return nil, ExitFailure
	}

	if err := reg.Refresh(ctx); err != nil {
		m.Ui.Warn(fmt.Sprintf("Some providers did not answer: %s", err))
	}

	return reg, ExitSuccess
}
