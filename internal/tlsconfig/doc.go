// Package tlsconfig builds the TLS for the server's two listeners and for the
// clients that dial them, so the API, the agent listener, the CLI and the agent
// share one set of defaults.
//
// TLS 1.3 is the minimum. Every client is Go, and 1.3 leaves no cipher suites
// to choose. A CA is a PEM file of one or more certificates, or a directory of
// them.
package tlsconfig
