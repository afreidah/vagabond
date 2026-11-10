// -------------------------------------------------------------------------------
// TLS Flag Tests
//
// Author: Alex Freidah
//
// The -tls flags as the client commands and the agent read them, the API's TLS
// from the server block, and client commands against a server that requires a
// client certificate, over real handshakes.
// -------------------------------------------------------------------------------

package cli

import (
	"crypto/tls"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/tlsconfig/tlstest"
)

// -------------------------------------------------------------------------
// CLIENT FLAGS
// -------------------------------------------------------------------------

// No -tls flag is no TLS, and a certificate and key come together.
func TestTLSFlags_Config(t *testing.T) {
	if cfg, err := (&tlsFlags{}).config("host"); cfg != nil || err != nil {
		t.Errorf("no flags: config() = %v, %v; want no TLS", cfg, err)
	}

	for name, flags := range map[string]tlsFlags{
		"a certificate, no key": {cert: "client.pem"},
		"a key, no certificate": {key: "client-key.pem"},
	} {
		if _, err := flags.config(""); err == nil || !strings.Contains(err.Error(), "go together") {
			t.Errorf("%s: config() = %v, want the pair refused", name, err)
		}
	}
}

// -tls-server-name wins over the name the caller falls back to.
func TestTLSFlags_ServerName(t *testing.T) {
	ca := tlstest.NewCA(t)

	cfg, err := (&tlsFlags{ca: ca.File}).config("fallback")
	if err != nil || cfg.ServerName != "fallback" {
		t.Errorf("config() = %v, %v; want the fallback name", cfg, err)
	}

	cfg, err = (&tlsFlags{ca: ca.File, serverName: "named"}).config("fallback")
	if err != nil || cfg.ServerName != "named" {
		t.Errorf("config() = %v, %v; want -tls-server-name", cfg, err)
	}
}

// -------------------------------------------------------------------------
// CLIENT COMMANDS
// -------------------------------------------------------------------------

// A command presenting a certificate the server's CA signed is served; one
// presenting none is told the server requires one.
func TestClientCommands_MutualTLS(t *testing.T) {
	ca := tlstest.NewCA(t)
	s := serveTLS(t, functionConfig, ca)
	certFile, keyFile := ca.Client(t, "ci")

	code, stdout, stderr := s.at([]string{"provider", "status"}, "-tls-ca", ca.File, "-tls-cert", certFile, "-tls-key", keyFile)
	if code != ExitSuccess || !strings.Contains(stdout, "fn") {
		t.Errorf("with a certificate: exit %d\n%s%s", code, stdout, stderr)
	}

	code, _, stderr = s.at([]string{"provider", "status"}, "-tls-ca", ca.File)
	if code != ExitFailure || !strings.Contains(stderr, "requires a client certificate") {
		t.Errorf("without a certificate: exit %d\n%s", code, stderr)
	}
}

// Each -tls flag defaults to its environment variable.
func TestClientCommands_TLSFromEnvironment(t *testing.T) {
	ca := tlstest.NewCA(t)
	s := serveTLS(t, functionConfig, ca)
	certFile, keyFile := ca.Client(t, "ci")

	t.Setenv(tlsCAEnv, ca.File)
	t.Setenv(tlsCertEnv, certFile)
	t.Setenv(tlsKeyEnv, keyFile)
	t.Setenv(tlsServerNameEnv, "localhost")

	if code, stdout, stderr := s.at([]string{"node", "status"}); code != ExitSuccess {
		t.Errorf("exit %d\n%s%s", code, stdout, stderr)
	}
}

// TLS with an http address, a certificate without its key, and a CA that
// cannot be read are refused before anything is contacted.
func TestClientCommands_TLSMisuse(t *testing.T) {
	clearClientEnv(t)

	ca := tlstest.NewCA(t)
	missing := filepath.Join(t.TempDir(), "missing.pem")

	tests := map[string]struct {
		args []string
		want string
	}{
		"http address":           {args: []string{"-address", "http://127.0.0.1:1", "-tls-ca", ca.File}, want: "https://"},
		"address with no scheme": {args: []string{"-address", "127.0.0.1:1", "-tls-server-name", "vagabond"}, want: "https://"},
		"certificate, no key":    {args: []string{"-address", "https://127.0.0.1:1", "-tls-cert", "client.pem"}, want: "go together"},
		"unreadable CA":          {args: []string{"-address", "https://127.0.0.1:1", "-tls-ca", missing}, want: "loading the CA from"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := run(append([]string{"node", "status"}, tt.args...)...)
			if code != ExitFailure || !strings.Contains(stdout+stderr, tt.want) {
				t.Errorf("exit %d, want a failure mentioning %q:\n%s%s", code, tt.want, stdout, stderr)
			}
		})
	}
}

// -namespace is taken only by the commands that act in one.
func TestClientCommands_NamespaceOnlyWhereUsed(t *testing.T) {
	for _, words := range [][]string{
		{"node", "status"}, {"provider", "status"}, {"execution", "status"}, {"execution", "logs"},
	} {
		code, stdout, stderr := run(append(words, "-namespace", "ci")...)
		if code != ExitFailure || !strings.Contains(stdout+stderr, "-namespace") {
			t.Errorf("%s: exit %d, want -namespace refused:\n%s%s", strings.Join(words, " "), code, stdout, stderr)
		}
	}
}

// Every client command's help ends with the client options.
func TestClientCommands_HelpListsClientOptions(t *testing.T) {
	for _, words := range [][]string{{"job", "run"}, {"node", "status"}, {"execution", "logs"}} {
		_, stdout, stderr := run(append(words, "-help")...)

		for _, want := range []string{"Client Options:", "-address <addr>", "-tls-ca <path>", "-tls-server-name <name>"} {
			if !strings.Contains(stdout+stderr, want) {
				t.Errorf("%s help does not mention %q", strings.Join(words, " "), want)
			}
		}
	}
}

// -------------------------------------------------------------------------
// SERVER
// -------------------------------------------------------------------------

// No tls block is plain HTTP; a ca verifies client certificates when given.
func TestAPITLS(t *testing.T) {
	ca := tlstest.NewCA(t)
	certFile, keyFile := ca.Server(t)

	if cfg, err := apiTLS(nil); cfg != nil || err != nil {
		t.Errorf("no server block: apiTLS() = %v, %v; want plain HTTP", cfg, err)
	}

	cfg, err := apiTLS(&config.ServerBlock{TLS: &config.TLSBlock{Cert: certFile, Key: keyFile}})
	if err != nil || cfg.ClientCAs != nil {
		t.Errorf("no ca: apiTLS() = %v, %v; want no client verification", cfg, err)
	}

	cfg, err = apiTLS(&config.ServerBlock{TLS: &config.TLSBlock{Cert: certFile, Key: keyFile, CA: ca.File}})
	if err != nil || cfg.ClientCAs == nil || cfg.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Errorf("with a ca: apiTLS() = %v, %v; want certificates verified when given", cfg, err)
	}
}

// An API reachable beyond this machine with no client CA is warned about,
// and a tls block whose certificate cannot be read stops the server.
func TestServer_APITLS(t *testing.T) {
	code, stderr := startServer(t, `
provider "fn" { type = "fake-function" }

server {
  bind       = "0.0.0.0:0"
  agent_bind = "0.0.0.0:0"
}
`, "-dev")

	if code != ExitFailure || !strings.Contains(stderr, "accepts any client") {
		t.Errorf("open API: exit %d, stderr:\n%s", code, stderr)
	}

	code, stderr = startServer(t, `
provider "fn" { type = "fake-function" }

server {
  tls {
    cert = "missing.pem"
    key  = "missing-key.pem"
  }
}
`, "-dev")

	if code != ExitFailure || !strings.Contains(stderr, "loading the certificate missing.pem") {
		t.Errorf("unreadable certificate: exit %d, stderr:\n%s", code, stderr)
	}
}

// -------------------------------------------------------------------------
// AGENT
// -------------------------------------------------------------------------

// The agent's CA, certificate and key come together or not at all, and a
// server name alone is refused.
func TestAgentTLS_AllOrNone(t *testing.T) {
	tests := map[string]struct {
		certs tlsFlags
		want  string
	}{
		"only a CA":               {certs: tlsFlags{ca: "ca.pem"}, want: "all three or none"},
		"a certificate, no key":   {certs: tlsFlags{ca: "ca.pem", cert: "agent.pem"}, want: "all three or none"},
		"a server name, no certs": {certs: tlsFlags{serverName: "vagabond"}, want: "-tls-server-name needs"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := agentTLS(&tt.certs, "10.0.0.5:4748"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("agentTLS() = %v, want it to mention %q", err, tt.want)
			}
		})
	}

	if cfg, err := agentTLS(&tlsFlags{}, "10.0.0.5:4748"); cfg != nil || err != nil {
		t.Errorf("no flags: agentTLS() = %v, %v; want no TLS", cfg, err)
	}
}

// With TLS the node is named for its certificate, a -name must match it, and
// the server's certificate must carry the dialled host unless told otherwise.
func TestIdentify(t *testing.T) {
	ca := tlstest.NewCA(t)
	certFile, keyFile := ca.Client(t, "box1")
	certs := tlsFlags{ca: ca.File, cert: certFile, key: keyFile}

	cfg, name, err := identify(&certs, "10.0.0.5:4748", "hostname", false)
	if err != nil || name != "box1" || cfg.ServerName != "10.0.0.5" {
		t.Errorf("identify() = %v, %q, %v; want box1 dialling 10.0.0.5", cfg, name, err)
	}

	if _, name, err := identify(&certs, "10.0.0.5:4748", "box1", true); err != nil || name != "box1" {
		t.Errorf("matching -name: identify() = %q, %v", name, err)
	}

	if _, _, err := identify(&certs, "10.0.0.5:4748", "box2", true); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("another -name: identify() = %v, want it refused", err)
	}

	if _, _, err := identify(&certs, "no-port", "hostname", false); err == nil {
		t.Error("an address with no port and no -tls-server-name: identify() = nil error")
	}

	named := certs
	named.serverName = "vagabond"

	if cfg, _, err := identify(&named, "no-port", "hostname", false); err != nil || cfg.ServerName != "vagabond" {
		t.Errorf("with -tls-server-name: identify() = %v, %v", cfg, err)
	}

	if cfg, name, err := identify(&tlsFlags{}, "10.0.0.5:4748", "hostname", false); cfg != nil || name != "hostname" || err != nil {
		t.Errorf("no TLS: identify() = %v, %q, %v; want the name as given", cfg, name, err)
	}
}
