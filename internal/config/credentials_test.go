// -------------------------------------------------------------------------------
// Credential Tests
//
// Author: Alex Freidah
//
// Two properties matter here and the rest is plumbing. A block naming two
// sources must be refused, because whichever won would silently decide what a
// provider authenticates with. And a helper's trailing newline must not reach
// the credential, because an API key with a newline on the end fails
// authentication in a way nobody diagnoses quickly.
// -------------------------------------------------------------------------------

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/ptr"
)

// resolve is the common shape: resolve a block and fail on any error.
func resolve(t *testing.T, c *CredentialsBlock) string {
	t.Helper()

	secret, err := c.Resolve(t.Context())
	if err != nil {
		t.Fatalf("resolving failed: %s", err)
	}

	return string(secret)
}

// -------------------------------------------------------------------------
// SOURCES
// -------------------------------------------------------------------------

func TestResolveFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"type":"service_account"}`), 0o600); err != nil {
		t.Fatalf("writing the key: %s", err)
	}

	got := resolve(t, &CredentialsBlock{File: ptr.Of(path)})
	if got != `{"type":"service_account"}` {
		t.Errorf("secret = %q, want the file's contents", got)
	}
}

func TestResolveEnv(t *testing.T) {
	t.Setenv("VAGABOND_TEST_SECRET", "a-secret")

	got := resolve(t, &CredentialsBlock{Env: ptr.Of("VAGABOND_TEST_SECRET")})
	if got != "a-secret" {
		t.Errorf("secret = %q, want a-secret", got)
	}
}

func TestResolveExec(t *testing.T) {
	t.Parallel()

	got := resolve(t, &CredentialsBlock{Exec: []string{"printf", "%s", "from-a-command"}})
	if got != "from-a-command" {
		t.Errorf("secret = %q, want from-a-command", got)
	}
}

// A nil block is not an error. Most providers need no credential and should
// not have to declare an empty block to say so.
func TestResolveNilBlock(t *testing.T) {
	t.Parallel()

	var c *CredentialsBlock

	secret, err := c.Resolve(t.Context())
	if err != nil {
		t.Fatalf("a nil block failed to resolve: %s", err)
	}

	if secret != nil {
		t.Errorf("secret = %q, want nothing", secret)
	}
}

// -------------------------------------------------------------------------
// THE NEWLINE
// -------------------------------------------------------------------------

// A helper that prints its answer adds a newline, and a trailing newline in an
// API key is an authentication failure that looks like a wrong key.
func TestResolveExecTrimsTrailingNewlines(t *testing.T) {
	t.Parallel()

	got := resolve(t, &CredentialsBlock{Exec: []string{"echo", "a-secret"}})
	if got != "a-secret" {
		t.Errorf("secret = %q, want the newline trimmed", got)
	}
}

// Only the trailing newline, though. A service account JSON is multi-line and
// stripping inside it would corrupt the key.
func TestResolveExecKeepsInteriorNewlines(t *testing.T) {
	t.Parallel()

	got := resolve(t, &CredentialsBlock{Exec: []string{"printf", "line one\nline two\n"}})
	if got != "line one\nline two" {
		t.Errorf("secret = %q, want the interior newline kept", got)
	}
}

// -------------------------------------------------------------------------
// FAILURE
// -------------------------------------------------------------------------

func TestResolveFailures(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		block *CredentialsBlock
		want  string
	}{
		"missing file": {
			block: &CredentialsBlock{File: ptr.Of("/nowhere/key.json")},
			want:  "/nowhere/key.json",
		},
		"unset variable": {
			block: &CredentialsBlock{Env: ptr.Of("VAGABOND_DEFINITELY_UNSET")},
			want:  "VAGABOND_DEFINITELY_UNSET",
		},
		"command that fails": {
			block: &CredentialsBlock{Exec: []string{"false"}},
			want:  "false",
		},
		"command that does not exist": {
			block: &CredentialsBlock{Exec: []string{"vagabond-no-such-helper"}},
			want:  "vagabond-no-such-helper",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.block.Resolve(t.Context())
			if err == nil {
				t.Fatal("expected resolution to fail")
			}

			// The error names what was tried, because the operator reading it
			// is looking for a typo in a path or a variable name.
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A helper's complaint is worth surfacing: "permission denied" from Vault is
// the whole diagnosis, and exit status 1 alone is not.
func TestResolveExecReportsStderr(t *testing.T) {
	t.Parallel()

	block := &CredentialsBlock{
		Exec: []string{"sh", "-c", "echo 'permission denied' >&2; exit 1"},
	}

	_, err := block.Resolve(t.Context())
	if err == nil {
		t.Fatal("expected the command to fail")
	}

	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error does not carry what the helper said: %s", err)
	}
}

// A warning on standard error must not end up inside the secret.
func TestResolveExecIgnoresStderrOnSuccess(t *testing.T) {
	t.Parallel()

	got := resolve(t, &CredentialsBlock{
		Exec: []string{"sh", "-c", "echo 'a warning' >&2; printf 'the-secret'"},
	})

	if got != "the-secret" {
		t.Errorf("secret = %q, want the-secret", got)
	}
}

// An empty block reaching Resolve is a bug rather than a configuration error,
// since validation refuses it first, so it is a sentinel rather than a
// diagnostic.
func TestResolveEmptyBlock(t *testing.T) {
	t.Parallel()

	_, err := (&CredentialsBlock{}).Resolve(t.Context())
	if !errors.Is(err, ErrNoCredentialSource) {
		t.Errorf("err = %v, want ErrNoCredentialSource", err)
	}
}

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

// Two sources is the dangerous case: whichever won would decide what the
// provider authenticates with, and the author would not know which.
func TestValidateRejectsAmbiguousSource(t *testing.T) {
	t.Parallel()

	got := loadErr(t, `
provider "gcp" {
  type = "cloud-run"

  credentials {
    file = "/etc/vagabond/key.json"
    env  = "GOOGLE_CREDENTIALS"
  }
}
`)

	if !strings.Contains(got, "Ambiguous credential source") {
		t.Errorf("unexpected diagnostics: %s", got)
	}

	// Naming both makes the fix obvious.
	for _, want := range []string{"file", "env"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostics do not name %q: %s", want, got)
		}
	}
}

func TestValidateRejectsEmptyCredentialsBlock(t *testing.T) {
	t.Parallel()

	got := loadErr(t, `
provider "gcp" {
  type = "cloud-run"
  credentials {}
}
`)

	if !strings.Contains(got, "Missing credential source") {
		t.Errorf("unexpected diagnostics: %s", got)
	}
}

func TestValidateAcceptsNoCredentialsBlock(t *testing.T) {
	t.Parallel()

	load(t, `provider "fake" { type = "fake-container" }`)
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

func TestCredentialsDecode(t *testing.T) {
	t.Parallel()

	file := load(t, `
provider "from-file" {
  type = "cloud-run"
  credentials { file = "/etc/vagabond/key.json" }
}

provider "from-env" {
  type = "cloud-run"
  credentials { env = "GOOGLE_CREDENTIALS" }
}

provider "from-exec" {
  type = "cloud-run"

  credentials {
    exec = ["vault", "kv", "get", "-field=credentials_json", "secret/vagabond/cloud-run"]
  }
}
`)

	if got := ptr.Deref(file.Providers[0].Credentials.File); got != "/etc/vagabond/key.json" {
		t.Errorf("file = %q", got)
	}

	if got := ptr.Deref(file.Providers[1].Credentials.Env); got != "GOOGLE_CREDENTIALS" {
		t.Errorf("env = %q", got)
	}

	if got := len(file.Providers[2].Credentials.Exec); got != 5 {
		t.Errorf("exec has %d words, want 5", got)
	}
}
