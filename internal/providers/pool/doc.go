// Package pool is the provider for agent nodes: one instance per declared
// pool, whose members are the nodes that joined it. Admission judges each node
// on its own; the pool places work on the one with the most room and routes
// every later call to the node that holds it.
package pool
