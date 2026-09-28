// Package agentrpc is the protocol between a server and its agents: the
// generated gRPC bindings for agent.proto, and the session both sides run them
// over.
//
// The agent dials; the server may be unreachable from the agent's network in
// no direction but that one. One TCP connection is multiplexed with yamux, and
// each side serves gRPC on the streams its peer opens and calls its peer on
// streams it opens itself.
package agentrpc
