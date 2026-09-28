// Package agent is vagabond agent, the process that runs on a node apart from
// the server. It dials its server, registers the node into a pool, and runs
// what the server sends it on the node's containerd, under its own cgroup,
// until told to stop.
package agent
