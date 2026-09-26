// Package server is the long-running process jobs are submitted to: the
// provider registry, the stores and a dispatcher behind an HTTP/JSON API.
//
// A dispatch returns as soon as it has an ID and runs in its own goroutine.
// Shutdown drains requests but leaves running executions recorded as they
// were, for a restarted server to pick up, rather than cancelling them on
// every provider.
package server
