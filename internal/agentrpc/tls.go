// -------------------------------------------------------------------------------
// Agent Connection TLS
//
// Author: Alex Freidah
//
// Mutual TLS for the agent connection. The server presents its certificate and
// requires one from every agent, signed by the configured CA; the agent
// verifies the server against the same kind of CA. The agent certificate's
// common name is the node name it may register under.
// -------------------------------------------------------------------------------

package agentrpc

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/hashicorp/go-rootcerts"
)

// ServerTLS is the server's side: its certificate and key, and the CA every
// agent certificate must chain to.
func ServerTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading the agent listener's certificate: %w", err)
	}

	pool, err := loadCA(caFile)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ClientTLS is the agent's side: its certificate and key, the CA the server's
// certificate must chain to, and the name that certificate must carry.
func ClientTLS(certFile, keyFile, caFile, serverName string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading the agent's certificate: %w", err)
	}

	pool, err := loadCA(caFile)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// Identity is the common name of the certificate cfg presents, which is the
// node name an agent holding it registers under.
func Identity(cfg *tls.Config) (string, error) {
	if len(cfg.Certificates) == 0 || cfg.Certificates[0].Leaf == nil {
		return "", errors.New("no certificate to take a name from")
	}

	name := cfg.Certificates[0].Leaf.Subject.CommonName
	if name == "" {
		return "", errors.New("the agent's certificate has no common name")
	}

	return name, nil
}

// loadCA reads a PEM file of one or more CA certificates.
func loadCA(path string) (*x509.CertPool, error) {
	pool, err := rootcerts.LoadCAFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading the CA from %s: %w", path, err)
	}

	return pool, nil
}
