// Package nodes holds the agent nodes connected to a server and the room each
// has left: it serves the agents' connections, adds and removes nodes as they
// connect and drop, and counts room once per node, so every provider drawing
// on a node places against the same count.
package nodes
