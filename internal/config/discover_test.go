// -------------------------------------------------------------------------------
// Discovery Tests
//
// Author: Alex Freidah
//
// The property that matters is that a path someone named explicitly is never
// quietly replaced by one they did not. Planning against a different set of
// providers than the operator asked for is the worst thing this package can do,
// and it would look like a correct plan.
// -------------------------------------------------------------------------------

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -------------------------------------------------------------------------
// PRECEDENCE
// -------------------------------------------------------------------------

func TestDiscoverPrefersTheFlag(t *testing.T) {
	flagged := writeConfig(t, "flagged.hcl")
	t.Setenv(EnvConfig, writeConfig(t, "environment.hcl"))

	got, err := Discover(flagged)
	if err != nil {
		t.Fatalf("discovery failed: %s", err)
	}

	if got != flagged {
		t.Errorf("discovered %q, want the flagged path %q", got, flagged)
	}
}

func TestDiscoverFallsBackToTheEnvironment(t *testing.T) {
	fromEnv := writeConfig(t, "environment.hcl")
	t.Setenv(EnvConfig, fromEnv)

	got, err := Discover("")
	if err != nil {
		t.Fatalf("discovery failed: %s", err)
	}

	if got != fromEnv {
		t.Errorf("discovered %q, want %q", got, fromEnv)
	}
}

func TestDiscoverUsesTheSearchPath(t *testing.T) {
	t.Setenv(EnvConfig, "")

	// The working directory is first on the search path, so a config beside
	// the job being planned wins without anyone saying so.
	t.Chdir(t.TempDir())

	if err := os.WriteFile(workingDirConfig, []byte(sampleConfig), 0o600); err != nil {
		t.Fatalf("writing config: %s", err)
	}

	got, err := Discover("")
	if err != nil {
		t.Fatalf("discovery failed: %s", err)
	}

	if got != workingDirConfig {
		t.Errorf("discovered %q, want %q", got, workingDirConfig)
	}
}

// -------------------------------------------------------------------------
// FAILURE
// -------------------------------------------------------------------------

// A caller who named a file meant that file. Falling through to something else
// would produce a plan describing providers they did not configure.
func TestDiscoverRefusesAMissingNamedPath(t *testing.T) {
	t.Setenv(EnvConfig, "")
	t.Chdir(t.TempDir())

	tests := map[string]struct {
		flag string
		env  string
		want string
	}{
		"flag": {flag: "nowhere.hcl", want: "-config"},
		"env":  {env: "nowhere.hcl", want: EnvConfig},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv(EnvConfig, tc.env)
			}

			_, err := Discover(tc.flag)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}

			// Naming who asked for it is the difference between a useful error
			// and one that sends someone looking in the wrong place.
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not name %s: %s", tc.want, err)
			}
		})
	}
}

func TestDiscoverReportsWhereItLooked(t *testing.T) {
	t.Setenv(EnvConfig, "")
	t.Chdir(t.TempDir())

	_, err := Discover("")
	if !errors.Is(err, ErrNoConfig) {
		t.Fatalf("err = %v, want ErrNoConfig", err)
	}

	// Every path tried, so the error teaches the search path rather than
	// leaving someone to guess at it.
	for _, path := range SearchPath() {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error omits %q: %s", path, err)
		}
	}

	if !strings.Contains(err.Error(), EnvConfig) {
		t.Errorf("error does not mention %s: %s", EnvConfig, err)
	}
}

func TestSearchPathOrder(t *testing.T) {
	t.Parallel()

	paths := SearchPath()

	if paths[0] != workingDirConfig {
		t.Errorf("first search path is %q, want the working directory", paths[0])
	}

	if paths[len(paths)-1] != systemConfig {
		t.Errorf("last search path is %q, want %q", paths[len(paths)-1], systemConfig)
	}
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

const sampleConfig = `provider "container-primary" { type = "fake-container" }`

// writeConfig puts a usable configuration at a temporary path.
func writeConfig(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)

	if err := os.WriteFile(path, []byte(sampleConfig), 0o600); err != nil {
		t.Fatalf("writing %s: %s", path, err)
	}

	return path
}
