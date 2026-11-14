// Package pool is the provider over a pool of agent nodes, type containerd:
// one instance per declared provider, whose members are the nodes that joined
// its pool. Admission judges each node on its own and ranking places each as a
// candidate; every call for an execution goes to the node its record names.
package pool
