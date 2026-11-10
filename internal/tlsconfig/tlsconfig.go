// -------------------------------------------------------------------------------
// TLS Configuration
//
// Author: Alex Freidah
//
// A listener presents its certificate and, given a CA, verifies its clients'
// certificates against it. A client verifies the server against a CA, or the
// system's roots without one, and presents its own certificate when it has
// one.
// -------------------------------------------------------------------------------

package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/hashicorp/go-rootcerts"
)

// -------------------------------------------------------------------------
// CONFIGURATIONS
// -------------------------------------------------------------------------

// Server is a listener's TLS: the certificate and key it presents and, when
// caFile is set, the CA client certificates are verified against under auth.
// Without a CA no client certificate is asked for.
func Server(certFile, keyFile, caFile string, auth tls.ClientAuthType) (*tls.Config, error) {
	cert, err := loadPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}

	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}

	if caFile != "" {
		if cfg.ClientCAs, err = loadCA(caFile); err != nil {
			return nil, err
		}

		cfg.ClientAuth = auth
	}

	return cfg, nil
}

// Client is a dialer's TLS: the CA the server's certificate must chain to, or
// the system's roots when caFile is empty; the certificate and key it presents,
// when both are set; and the name the server's certificate must carry. An
// HTTPS client may leave serverName to the URL's host.
func Client(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS13}

	if caFile != "" {
		pool, err := loadCA(caFile)
		if err != nil {
			return nil, err
		}

		cfg.RootCAs = pool
	}

	if certFile != "" || keyFile != "" {
		cert, err := loadPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}

		cfg.Certificates = []tls.Certificate{cert}
	}

	return cfg, nil
}

// -------------------------------------------------------------------------
// LOADING
// -------------------------------------------------------------------------

// loadPair reads a certificate and its key.
func loadPair(certFile, keyFile string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("loading the certificate %s: %w", certFile, err)
	}

	return cert, nil
}

// loadCA reads the CA certificates in path, a PEM file or a directory of them.
func loadCA(path string) (*x509.CertPool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("loading the CA from %s: %w", path, err)
	}

	load := rootcerts.LoadCAFile
	if info.IsDir() {
		load = rootcerts.LoadCAPath
	}

	pool, err := load(path)
	if err != nil {
		return nil, fmt.Errorf("loading the CA from %s: %w", path, err)
	}

	return pool, nil
}
