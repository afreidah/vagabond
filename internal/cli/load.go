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
	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobspec"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/registry"
	"github.com/afreidah/vagabond/internal/state/postgres"
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
// The store block comes back beside the registry for loadLedger.
//
// A refresh failure is reported but does not stop the command. A provider that
// did not answer is marked unhealthy and rejected by name, which is more useful
// than refusing to proceed at all: the other providers still have answers.
func (m *Meta) loadRegistry(
	ctx context.Context, configPath string,
) (*registry.Registry, *config.StoreBlock, int) {
	path, err := config.Discover(configPath)
	if err != nil {
		return nil, nil, m.Errorf("%s", err)
	}

	cfg, diags := config.LoadPath(path)
	if diags.HasErrors() {
		renderDiagnostics(m.Ui, nil, diags, m.color())

		return nil, nil, ExitFailure
	}

	reg, diags := registry.New(ctx, cfg)
	if diags.HasErrors() {
		renderDiagnostics(m.Ui, nil, diags, m.color())

		return nil, nil, ExitFailure
	}

	if err := reg.Refresh(ctx); err != nil {
		m.Ui.Warn(fmt.Sprintf("Some providers did not answer: %s", err))
	}

	return reg, cfg.Store, ExitSuccess
}

// loadLedger builds the ledger a command prices and charges against, and the
// function that finishes with it.
//
// No store block is an in-memory ledger; the caller decides whether that is
// worth a warning. A store that cannot be opened fails the command unless
// untracked was given, because pricing against an empty ledger reads every
// provider as full and dispatching against one spends without a record. A
// store that opens is always used, untracked or not.
//
// action names what -untracked would let the command do, for the error.
func (m *Meta) loadLedger(
	ctx context.Context, store *config.StoreBlock, reg *registry.Registry,
	untracked bool, action string,
) (dispatch.Ledger, func(), int) {
	if store == nil {
		return m.memoryLedger(ctx, reg)
	}

	led, closeStore, err := openLedger(ctx, store.DSN, reg)
	if err == nil {
		return led, closeStore, ExitSuccess
	}

	if !untracked {
		return nil, nil, m.Errorf(
			"Could not open the usage store: %s\n\nPass -untracked to %s without recording usage.",
			err, action)
	}

	m.Ui.Warn(fmt.Sprintf(
		"Could not open the usage store: %s\nContinuing untracked: usage is not recorded.", err))

	return m.memoryLedger(ctx, reg)
}

// memoryLedger is a ledger whose usage leaves with the process.
func (m *Meta) memoryLedger(ctx context.Context, reg *registry.Registry) (dispatch.Ledger, func(), int) {
	led, err := ledger.New(ctx, reg.Limits(), ledger.NewMemory(nil))
	if err != nil {
		return nil, nil, m.Errorf("%s", err)
	}

	return led, func() {}, ExitSuccess
}

// openLedger connects, migrates, and loads the stored usage, as
// s3-orchestrator's store provider does at startup. The function returned
// closes the connection.
func openLedger(
	ctx context.Context, dsn string, reg *registry.Registry,
) (*ledger.Ledger, func(), error) {
	db, err := postgres.Open(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}

	if err := db.Migrate(ctx); err != nil {
		db.Close()

		return nil, nil, err
	}

	if err := db.VerifySchema(ctx); err != nil {
		db.Close()

		return nil, nil, err
	}

	led, err := ledger.New(ctx, reg.Limits(), db)
	if err != nil {
		db.Close()

		return nil, nil, err
	}

	return led, db.Close, nil
}
