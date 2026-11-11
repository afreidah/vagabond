// -------------------------------------------------------------------------------
// Agent Certificate Identity
//
// Author: Alex Freidah
//
// Over mutual TLS an agent's certificate is its identity: the common name is
// the node name it may register under, so the agent takes its name from the
// certificate it presents and the server checks it against the one it
// verified.
// -------------------------------------------------------------------------------

package agentrpc

import (
	"crypto/tls"
	"errors"
)

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
