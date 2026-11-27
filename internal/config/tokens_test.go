// -------------------------------------------------------------------------------
// API Token Tests
//
// Author: Alex Freidah
//
// Token blocks decode with their namespaces and policies, are refused when they
// would mean nothing, and yield their secret trimmed and long enough.
// -------------------------------------------------------------------------------

package config

import (
	"strings"
	"testing"
)

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// A management token and a token scoped to namespaces both decode, each
// namespace with its policy.
func TestLoadDecodesTokens(t *testing.T) {
	t.Parallel()

	file := load(t, `
namespace "ci" {}

token "admin" {
  credentials { env = "ADMIN_TOKEN" }
  management = true
}

token "ci" {
  credentials { file = "/secrets/ci-token" }

  namespace "ci" {
    policy = "write"
  }

  namespace "default" {
    policy = "read"
  }
}
`)

	if len(file.Tokens) != 2 {
		t.Fatalf("tokens = %+v, want admin and ci", file.Tokens)
	}

	admin, ci := file.Tokens[0], file.Tokens[1]

	if admin.Name != "admin" || !admin.Management || *admin.Credentials.Env != "ADMIN_TOKEN" {
		t.Errorf("admin = %+v, want a management token read from ADMIN_TOKEN", admin)
	}

	want := []TokenNamespace{{Name: "ci", Policy: PolicyWrite}, {Name: "default", Policy: PolicyRead}}
	if ci.Management || len(ci.Namespaces) != 2 || ci.Namespaces[0] != want[0] || ci.Namespaces[1] != want[1] {
		t.Errorf("ci = %+v, want write in ci and read in default", ci)
	}
}

// A token may live in its own file, beside the namespace it names.
func TestLoadPathMergesTokens(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	write(t, dir, "fn.hcl", "provider \"fn\" { type = \"fake-function\" }\nnamespace \"ci\" {}")
	write(t, dir, "tokens.hcl", `
token "ci" {
  credentials { env = "CI_TOKEN" }

  namespace "ci" {
    policy = "read"
  }
}
`)

	file, diags := LoadPath(dir)
	if diags.HasErrors() {
		t.Fatalf("loading %s failed: %s", dir, diags.Error())
	}

	if len(file.Tokens) != 1 || file.Tokens[0].Name != "ci" {
		t.Errorf("tokens = %+v, want ci", file.Tokens)
	}
}

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

// Tokens declared twice, and tokens without a secret, with an ambiguous one,
// with both or neither of management and namespaces, or naming a namespace
// that is not declared, twice, or with an unknown policy, are refused.
func TestTokenValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "duplicate token",
			src: `token "ci" {
  credentials { env = "A" }
  management  = true
}

token "ci" {
  credentials { env = "B" }
  management  = true
}`,
			want: `Two tokens are named "ci"`,
		},
		{
			name: "no secret",
			src:  `token "ci" { management = true }`,
			want: `Token "ci" has no credentials block`,
		},
		{
			name: "two secret sources",
			src: `token "ci" {
  credentials {
    env  = "A"
    file = "/a"
  }
  management = true
}`,
			want: `Token "ci" names file and env`,
		},
		{
			name: "management with namespaces",
			src: `token "ci" {
  credentials { env = "A" }
  management = true

  namespace "default" {
    policy = "read"
  }
}`,
			want: "its namespace blocks mean nothing",
		},
		{
			name: "allows nothing",
			src: `token "ci" {
  credentials { env = "A" }
}`,
			want: `Token "ci" names no namespace and is not a management token`,
		},
		{
			name: "unknown namespace",
			src: `token "ci" {
  credentials { env = "A" }

  namespace "prod" {
    policy = "read"
  }
}`,
			want: `names namespace "prod", which is not declared`,
		},
		{
			name: "namespace named twice",
			src: `token "ci" {
  credentials { env = "A" }

  namespace "default" {
    policy = "read"
  }

  namespace "default" {
    policy = "write"
  }
}`,
			want: `names namespace "default" twice`,
		},
		{
			name: "unknown policy",
			src: `token "ci" {
  credentials { env = "A" }

  namespace "default" {
    policy = "admin"
  }
}`,
			want: `the policy "admin". Valid policies are read and write`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := loadErr(t, "provider \"fn\" { type = \"fake-function\" }\n"+tt.src); !strings.Contains(got, tt.want) {
				t.Errorf("diagnostics = %q, want them to mention %q", got, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// SECRET
// -------------------------------------------------------------------------

// A secret comes back without the whitespace around it. One too short once
// trimmed, or one that cannot be read, fails naming the token.
func TestTokenSecret(t *testing.T) {
	t.Setenv("VAGABOND_TEST_TOKEN", "  0123456789abcdef\n")
	t.Setenv("VAGABOND_TEST_SHORT", "short-secret    \n")

	tests := []struct {
		name    string
		env     string
		want    string
		wantErr string
	}{
		{name: "trimmed", env: "VAGABOND_TEST_TOKEN", want: "0123456789abcdef"},
		{name: "too short", env: "VAGABOND_TEST_SHORT", wantErr: `token "ci": the secret is 12 bytes, and must be at least 16`},
		{name: "unset", env: "VAGABOND_TEST_UNSET", wantErr: `token "ci": credential environment variable VAGABOND_TEST_UNSET is not set`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := &Token{Name: "ci", Credentials: &CredentialsBlock{Env: new(tt.env)}}

			got, err := tok.Secret(t.Context())

			switch {
			case tt.wantErr != "":
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("Secret() = %q, %v; want error %q", got, err, tt.wantErr)
				}
			case err != nil || got != tt.want:
				t.Errorf("Secret() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
