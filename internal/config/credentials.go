// -------------------------------------------------------------------------------
// Provider Credentials
//
// Author: Alex Freidah
//
// How a provider's secret is obtained, rather than the secret itself. Three
// sources cover every arrangement worth supporting, and the third covers the
// ones we have never heard of.
//
// Vagabond integrates with no secret store. Naming one in the schema would
// make the tool wrong for everyone who runs a different one and would drag its
// client library into the binary; exec delegates the whole question to a
// command the operator already trusts. kubectl does this for auth plugins and
// git for credential helpers, so it is a familiar shape rather than an
// invented one.
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

// execTimeout bounds a credential command.
//
// A helper that prompts for a password, or hangs against an unreachable secret
// store, would otherwise stall startup with no indication of why. Thirty
// seconds is long enough for a network round trip and short enough that the
// failure arrives while someone is still watching.
const execTimeout = 30 * time.Second

// ErrNoCredentialSource reports a credentials block naming none of the three
// sources.
var ErrNoCredentialSource = errors.New("no credential source")

// CredentialsBlock says where a provider's secret comes from.
//
// Exactly one source is set. There is deliberately no inline literal: someone
// would paste a key into the configuration and commit it, and File is one line
// more for a secret that stays out of the reviewed file.
type CredentialsBlock struct {
	File *string  `hcl:"file,optional"`
	Env  *string  `hcl:"env,optional"`
	Exec []string `hcl:"exec,optional"`
}

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

// validate reports a credentials block that names no source or several.
//
// Several is the dangerous one. A block naming both a file and an environment
// variable has an order of precedence the author is guessing at, and the
// provider it authenticates would be reached with whichever secret won.
// A nil block is valid: most providers need no credential and should not have
// to say so.
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

// Resolve returns the secret this block points at.
//
// Called once when the registry is built rather than per request, so a plugin
// holds bytes rather than a resolver and nothing reaches a secret store on the
// path a plan takes. A rotating secret therefore needs a refresh, which a
// long-running deployment can schedule and a single command never needs.
//
// A nil block resolves to nothing without error: a provider needing no
// credential, which every fake is, should not have to declare an empty one.
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
	// Cleaned so that the path reported back is the one that was read. This is
	// not a sandbox boundary: an operator who runs Vagabond can already read
	// their own files, and naming the file is the point of the setting.
	secret, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading credential from %s: %w", path, err)
	}

	return secret, nil
}

// readEnv reads a secret from the environment.
//
// An unset variable is an error rather than an empty secret, because the
// failure it would otherwise produce is an authentication rejection from the
// provider several steps later.
func readEnv(name string) ([]byte, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, fmt.Errorf("credential environment variable %s is not set", name)
	}

	return []byte(value), nil
}

// runExec obtains a secret from a command's standard output.
//
// The escape hatch, and the reason Vagabond needs no secret store client of
// its own: this covers Vault, 1Password, a cloud secret manager, pass, or a
// shell script nobody else has heard of.
//
// It is arbitrary code execution driven by a configuration file, which is
// acceptable only because the operator wrote that file. Anyone who can edit
// the provider config can already point Vagabond at a cloud account of their
// choosing, so the command is not the weakest link.
//
// Standard error is captured but never mixed into the secret. A helper that
// prints a warning would otherwise corrupt the credential in a way that
// surfaces as an unexplained authentication failure.
func runExec(ctx context.Context, argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	// Running a command from configuration is what this source is, so gosec's
	// warning is describing the feature. The command comes from a file the
	// operator wrote, and anyone able to edit it can already point Vagabond at
	// a cloud account of their choosing.
	//nolint:gosec // the configured command is the credential source
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)

	var stderr strings.Builder

	cmd.Stderr = &stderr

	secret, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("credential command %q: %w%s",
			strings.Join(argv, " "), err, describeStderr(stderr.String()))
	}

	// Trimmed because a helper that prints its answer adds a newline, and a
	// trailing newline in an API key is an authentication failure nobody
	// diagnoses quickly.
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
