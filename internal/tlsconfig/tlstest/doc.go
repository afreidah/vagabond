// Package tlstest mints a certificate authority per test, and the server and
// client certificates it signs, as files in the test's temporary directory, so
// TLS is tested against real handshakes rather than a stubbed configuration.
package tlstest
