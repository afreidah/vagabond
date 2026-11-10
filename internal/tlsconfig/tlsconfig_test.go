// -------------------------------------------------------------------------------
// TLS Configuration Tests
//
// Author: Alex Freidah
//
// What each side's configuration holds, against certificates minted per test.
// The handshakes themselves are exercised by the server's listener tests.
// -------------------------------------------------------------------------------

package tlsconfig

import (
	"crypto/tls"
	"path/filepath"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/tlsconfig/tlstest"
)

// -------------------------------------------------------------------------
// SERVER
// -------------------------------------------------------------------------

// Without a CA a listener presents its certificate and asks for none.
func TestServer_WithoutCA(t *testing.T) {
	certFile, keyFile := tlstest.NewCA(t).Server(t)

	cfg, err := Server(certFile, keyFile, "", tls.RequireAndVerifyClientCert)
	if err != nil {
		t.Fatalf("Server() = %v", err)
	}

	if len(cfg.Certificates) != 1 || cfg.ClientCAs != nil || cfg.ClientAuth != tls.NoClientCert {
		t.Errorf("config = %+v, want a certificate and no client verification", cfg)
	}

	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS 1.3", cfg.MinVersion)
	}
}

// With a CA, as a file or a directory, client certificates are verified
// against it as asked.
func TestServer_WithCA(t *testing.T) {
	ca := tlstest.NewCA(t)
	certFile, keyFile := ca.Server(t)

	for name, path := range map[string]string{"file": ca.File, "directory": ca.Dir} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Server(certFile, keyFile, path, tls.VerifyClientCertIfGiven)
			if err != nil {
				t.Fatalf("Server() = %v", err)
			}

			if cfg.ClientCAs == nil || cfg.ClientAuth != tls.VerifyClientCertIfGiven {
				t.Errorf("ClientCAs = %v, ClientAuth = %v; want the CA, verified if given", cfg.ClientCAs, cfg.ClientAuth)
			}
		})
	}
}

// A pair or a CA that cannot be read fails, naming the file.
func TestServer_Unreadable(t *testing.T) {
	ca := tlstest.NewCA(t)
	certFile, keyFile := ca.Server(t)
	missing := filepath.Join(t.TempDir(), "missing.pem")

	tests := map[string]struct {
		cert, key, ca, want string
	}{
		"missing certificate":   {cert: missing, key: keyFile, want: "loading the certificate " + missing},
		"mismatched key":        {cert: certFile, key: ca.File, want: "loading the certificate " + certFile},
		"missing CA":            {cert: certFile, key: keyFile, ca: missing, want: "loading the CA from " + missing},
		"CA that is not a cert": {cert: certFile, key: keyFile, ca: keyFile, want: "loading the CA from " + keyFile},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Server(tt.cert, tt.key, tt.ca, tls.RequireAndVerifyClientCert)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Server() = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// CLIENT
// -------------------------------------------------------------------------

// Without a CA or a certificate a client trusts the system's roots and
// presents nothing.
func TestClient_Bare(t *testing.T) {
	cfg, err := Client("", "", "", "vagabond.example")
	if err != nil {
		t.Fatalf("Client() = %v", err)
	}

	if cfg.RootCAs != nil || len(cfg.Certificates) != 0 || cfg.ServerName != "vagabond.example" {
		t.Errorf("config = %+v, want system roots, no certificate, the given name", cfg)
	}

	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS 1.3", cfg.MinVersion)
	}
}

// With a CA and a certificate a client verifies the server against the CA and
// presents its certificate.
func TestClient_Mutual(t *testing.T) {
	ca := tlstest.NewCA(t)
	certFile, keyFile := ca.Client(t, "ci")

	cfg, err := Client(ca.File, certFile, keyFile, "")
	if err != nil {
		t.Fatalf("Client() = %v", err)
	}

	if cfg.RootCAs == nil || len(cfg.Certificates) != 1 {
		t.Errorf("config = %+v, want the CA and one certificate", cfg)
	}
}

// A certificate without its key, or a CA that cannot be read, fails.
func TestClient_Unreadable(t *testing.T) {
	ca := tlstest.NewCA(t)
	certFile, _ := ca.Client(t, "ci")

	if _, err := Client("", certFile, "", ""); err == nil {
		t.Error("Client(certificate, no key) = nil error")
	}

	if _, err := Client(filepath.Join(t.TempDir(), "missing.pem"), "", "", ""); err == nil {
		t.Error("Client(missing CA) = nil error")
	}
}
