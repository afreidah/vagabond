// -------------------------------------------------------------------------------
// API Token Tests
//
// Author: Alex Freidah
//
// -token and $VAGABOND_TOKEN as client commands send them, and the server
// command turning token blocks into the tokens it checks: policies mapped,
// shared secrets refused, and the open-API warning quiet once tokens exist.
// -------------------------------------------------------------------------------

package cli

import (
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/server"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// tokenEnvVar holds the secret of the token tokenConfig declares.
const tokenEnvVar = "VAGABOND_TEST_CI_TOKEN"

// tokenSecret is the secret tests put in tokenEnvVar.
const tokenSecret = "ci-0123456789abcdef"

// tokenConfig is the function provider behind one token, which reads the
// default namespace.
const tokenConfig = functionConfig + `

token "ci" {
  credentials { env = "` + tokenEnvVar + `" }

  namespace "default" {
    policy = "read"
  }
}
`

// -------------------------------------------------------------------------
// CLIENT COMMANDS
// -------------------------------------------------------------------------

// A command is served with the token from -token or from $VAGABOND_TOKEN, and
// is told the server requires one without either.
func TestClientCommands_Token(t *testing.T) {
	t.Setenv(tokenEnvVar, tokenSecret)

	s := serve(t, tokenConfig)

	code, stdout, stderr := s.at([]string{"provider", "status"}, "-token", tokenSecret)
	if code != ExitSuccess || !strings.Contains(stdout, "fn") {
		t.Errorf("-token: exit %d\n%s%s", code, stdout, stderr)
	}

	code, _, stderr = s.at([]string{"provider", "status"})
	if code != ExitFailure || !strings.Contains(stderr, "requires an API token") {
		t.Errorf("no token: exit %d\n%s", code, stderr)
	}

	t.Setenv(tokenEnv, tokenSecret)

	if code, stdout, stderr := s.at([]string{"job", "status"}); code != ExitSuccess {
		t.Errorf("$VAGABOND_TOKEN: exit %d\n%s%s", code, stdout, stderr)
	}
}

// -------------------------------------------------------------------------
// SERVER
// -------------------------------------------------------------------------

// Each block becomes a token with its secret, a management flag or its
// namespaces at read or write. No blocks is no tokens.
func TestAPITokens(t *testing.T) {
	t.Setenv("VAGABOND_TEST_ADMIN", "admin-0123456789abcdef")
	t.Setenv("VAGABOND_TEST_CI", "ci-0123456789abcdef")

	tokens, err := apiTokens(t.Context(), []config.Token{
		{Name: "admin", Credentials: &config.CredentialsBlock{Env: new("VAGABOND_TEST_ADMIN")}, Management: true},
		{Name: "ci", Credentials: &config.CredentialsBlock{Env: new("VAGABOND_TEST_CI")}, Namespaces: []config.TokenNamespace{
			{Name: "default", Policy: config.PolicyRead},
			{Name: "ci", Policy: config.PolicyWrite},
		}},
	})
	if err != nil {
		t.Fatalf("apiTokens() = %v", err)
	}

	if len(tokens) != 2 {
		t.Fatalf("apiTokens() = %+v, want admin and ci", tokens)
	}

	admin, ci := tokens[0], tokens[1]

	if admin.Name != "admin" || admin.Secret != "admin-0123456789abcdef" || !admin.Management {
		t.Errorf("admin = %+v, want a management token", admin)
	}

	if ci.Management || ci.Namespaces["default"] != server.Read || ci.Namespaces["ci"] != server.Write {
		t.Errorf("ci = %+v, want read in default and write in ci", ci)
	}

	if none, err := apiTokens(t.Context(), nil); err != nil || len(none) != 0 {
		t.Errorf("apiTokens(nil) = %+v, %v; want none", none, err)
	}
}

// Two tokens with one secret are refused, naming both, and a secret that
// cannot be used stops everything.
func TestAPITokens_Refused(t *testing.T) {
	t.Setenv("VAGABOND_TEST_SHARED", "shared-0123456789abcdef")
	t.Setenv("VAGABOND_TEST_SHORT", "short")

	shared := &config.CredentialsBlock{Env: new("VAGABOND_TEST_SHARED")}

	_, err := apiTokens(t.Context(), []config.Token{
		{Name: "a", Credentials: shared, Management: true},
		{Name: "b", Credentials: shared, Management: true},
	})
	if err == nil || err.Error() != `tokens "a" and "b" have the same secret` {
		t.Errorf("shared secret: apiTokens() = %v, want both named", err)
	}

	_, err = apiTokens(t.Context(), []config.Token{
		{Name: "short", Credentials: &config.CredentialsBlock{Env: new("VAGABOND_TEST_SHORT")}, Management: true},
	})
	if err == nil || !strings.Contains(err.Error(), "must be at least 16") {
		t.Errorf("short secret: apiTokens() = %v, want it refused", err)
	}
}

// An API reachable beyond this machine is not warned about once a token is
// declared, and a token whose secret cannot be read stops the server.
func TestServer_Tokens(t *testing.T) {
	t.Setenv(tokenEnvVar, tokenSecret)

	code, stderr := startServer(t, tokenConfig+`
server {
  bind       = "0.0.0.0:0"
  agent_bind = "0.0.0.0:0"
}
`, "-dev")

	if code != ExitFailure || strings.Contains(stderr, "accepts any client") {
		t.Errorf("open API with a token: exit %d, stderr:\n%s", code, stderr)
	}

	t.Setenv(tokenEnvVar, "")

	if code, stderr := startServer(t, tokenConfig, "-dev"); code != ExitFailure || !strings.Contains(stderr, `token "ci"`) {
		t.Errorf("unusable secret: exit %d, stderr:\n%s", code, stderr)
	}
}
