// Package client is the node side of vagabond agent. It dials its server,
// registers the node into a pool, and runs what the server sends it on the
// node's containerd, under its own cgroup, until told to stop.
package client
