// -------------------------------------------------------------------------------
// Provider Credentials
//
// Author: Alex Freidah
//
// How a provider's secret is obtained, rather than the secret itself: from a
// file, an environment variable, or a command. Vagabond integrates with no
// secret store, so no client library enters the binary; exec delegates the
// question to a command the operator already trusts.
// -------------------------------------------------------------------------------

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// execTimeout bounds a credential command, so a helper that prompts or hangs
// fails while someone is still watching rather than stalling startup.
const execTimeout = 30 * time.Second

// ErrNoCredentialSource reports a credentials block naming none of the three
// sources.
var ErrNoCredentialSource = errors.New("no credential source")

// CredentialsBlock says where a provider's secret comes from. Exactly one
// source is set, and there is no inline literal to paste a key into.
type CredentialsBlock struct {
	File *string  `hcl:"file,optional"`
	Env  *string  `hcl:"env,optional"`
	Exec []string `hcl:"exec,optional"`
}

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

// validate reports a credentials block naming no source, or several and so a
// guessed precedence. A nil block is valid: most providers need no credential.
func (c *CredentialsBlock) validate(provider string) hcl.Diagnostics {
	if c == nil {
		return nil
	}

	sources := c.sources()

	switch {
	case len(sources) == 0:
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Missing credential source",
			Detail: fmt.Sprintf("Provider %q declares a credentials block naming "+
				"none of file, env or exec.", provider),
		}}

	case len(sources) > 1:
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Ambiguous credential source",
			Detail: fmt.Sprintf("Provider %q names %s. Exactly one source is "+
				"allowed, because which one wins would otherwise decide what "+
				"the provider authenticates with.",
				provider, strings.Join(sources, " and ")),
		}}
	}

	return nil
}

// sources lists the credential sources this block names.
func (c *CredentialsBlock) sources() []string {
	var named []string

	if c.File != nil {
		named = append(named, "file")
	}

	if c.Env != nil {
		named = append(named, "env")
	}

	if len(c.Exec) > 0 {
		named = append(named, "exec")
	}

	return named
}

// -------------------------------------------------------------------------
// RESOLUTION
// -------------------------------------------------------------------------

// Resolve returns the secret this block points at, called once when the
// registry is built so a plan never reaches a secret store. Nil yields nothing.
func (c *CredentialsBlock) Resolve(ctx context.Context) ([]byte, error) {
	if c == nil {
		return nil, nil
	}

	switch {
	case c.File != nil:
		return readFile(*c.File)

	case c.Env != nil:
		return readEnv(*c.Env)

	case len(c.Exec) > 0:
		return runExec(ctx, c.Exec)
	}

	return nil, ErrNoCredentialSource
}

// readFile reads a secret from disk.
func readFile(path string) ([]byte, error) {
	// Cleaned so the reported path is the one read; not a sandbox boundary
	secret, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading credential from %s: %w", path, err)
	}

	return secret, nil
}

// readEnv reads a secret from the environment. An unset variable is an error
// rather than an empty secret that the provider would reject much later.
func readEnv(name string) ([]byte, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, fmt.Errorf("credential environment variable %s is not set", name)
	}

	return []byte(value), nil
}

// runExec obtains a secret from a command's standard output, keeping standard
// error out of it so a printed warning cannot corrupt the credential.
func runExec(ctx context.Context, argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	// Anyone able to edit this file can already choose the cloud account
	//nolint:gosec // the configured command is the credential source
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)

	var stderr strings.Builder

	cmd.Stderr = &stderr

	secret, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("credential command %q: %w%s",
			strings.Join(argv, " "), err, describeStderr(stderr.String()))
	}

	// A helper's trailing newline would otherwise end up inside the key
	return []byte(strings.TrimRight(string(secret), "\r\n")), nil
}

// describeStderr renders what a failing credential command complained about.
func describeStderr(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}

	return ": " + stderr
}
