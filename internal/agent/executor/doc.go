// Package executor runs an agent's workloads. The containerd executor runs
// each as a container on the node's containerd, under the agent's cgroup, and
// keeps what it needs on the container so a restarted agent finds it.
package executor
