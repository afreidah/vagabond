// -------------------------------------------------------------------------------
// TLS Flags
//
// Author: Alex Freidah
//
// The -tls flags, shared by the agent and every command that talks to a
// server: the CA the server's certificate must chain to, the certificate and
// key this side presents, and the name the server's certificate must carry.
// The client commands default each from the environment; the agent, whose
// certificate comes from a different CA, does not.
// -------------------------------------------------------------------------------

package cli

import (
	"crypto/tls"
	"errors"
	"flag"
	"os"

	"github.com/afreidah/vagabond/internal/tlsconfig"
)

// Environment variables supplying the client commands' -tls flags.
const (
	tlsCAEnv         = "VAGABOND_TLS_CA"
	tlsCertEnv       = "VAGABOND_TLS_CERT"
	tlsKeyEnv        = "VAGABOND_TLS_KEY"
	tlsServerNameEnv = "VAGABOND_TLS_SERVER_NAME"
)

// tlsFlags is what the -tls flags name.
type tlsFlags struct {
	ca, cert, key, serverName string
}

// tlsFromEnv is the client commands' defaults for the -tls flags.
func tlsFromEnv() tlsFlags {
	return tlsFlags{
		ca:         os.Getenv(tlsCAEnv),
		cert:       os.Getenv(tlsCertEnv),
		key:        os.Getenv(tlsKeyEnv),
		serverName: os.Getenv(tlsServerNameEnv),
	}
}

// register adds the -tls flags to fs, each defaulting to its value in
// defaults.
func (f *tlsFlags) register(fs *flag.FlagSet, defaults tlsFlags) {
	fs.StringVar(&f.ca, "tls-ca", defaults.ca, "the CA file or directory the server's certificate chains to")
	fs.StringVar(&f.cert, "tls-cert", defaults.cert, "the certificate to present")
	fs.StringVar(&f.key, "tls-key", defaults.key, "the certificate's key")
	fs.StringVar(&f.serverName, "tls-server-name", defaults.serverName, "the name the server's certificate carries")
}

// set reports whether any -tls flag has a value.
func (f *tlsFlags) set() bool {
	return *f != (tlsFlags{})
}

// config builds the TLS for dialing a server, or nil when no -tls flag is set.
// The certificate and key go together, and fallbackName is the server's name
// when -tls-server-name is not given.
func (f *tlsFlags) config(fallbackName string) (*tls.Config, error) {
	switch {
	case !f.set():
		return nil, nil
	case (f.cert == "") != (f.key == ""):
		return nil, errors.New("-tls-cert and -tls-key go together: set both or neither")
	}

	name := f.serverName
	if name == "" {
		name = fallbackName
	}

	return tlsconfig.Client(f.ca, f.cert, f.key, name)
}
