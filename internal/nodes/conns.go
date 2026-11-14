// -------------------------------------------------------------------------------
// Node Connections
//
// Author: Alex Freidah
//
// Every agent node connected to this server, by name, with what it last
// reported, a client for calling its executions, and the room it has left.
// Room is counted here, once per node, whatever providers draw on it: a
// reservation holds it between placing a workload and the node reporting it.
// -------------------------------------------------------------------------------

package nodes

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/agentrpc"
)

// reservationTTL bounds how long a reservation covers for a node that has not
// reported the workload, so one lost to a failed call cannot hold room forever.
const reservationTTL = 2 * time.Minute

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Conn is one connected node: what it last reported, where it connected from
// and when, and a client for its executions. Session identifies which
// connection this is, since an agent that reconnects gets a new one.
type Conn struct {
	Node        *agentrpc.NodeRegisterRequest
	Executions  agentrpc.AgentExecutionsClient
	Address     string
	Established time.Time
	Session     *agentrpc.Session
}

// Conns is every connected node by name, and the room reserved on each by
// execution ID.
type Conns struct {
	now func() time.Time

	mu       sync.Mutex
	conns    map[string]*Conn
	reserved map[string]reservation
}

// reservation is room taken on a node for an execution it has not yet
// reported.
type reservation struct {
	node string
	need *agentrpc.Resources
	at   time.Time
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New returns an empty set of connections.
func New() *Conns {
	return &Conns{now: time.Now, conns: make(map[string]*Conn), reserved: make(map[string]reservation)}
}

// -------------------------------------------------------------------------
// CHANGES
// -------------------------------------------------------------------------

// Report records what a node reported on session. A first report adds the
// node; a later one on the same session updates it; one on a new session is
// the agent reconnecting, and replaces and closes the old connection. A
// workload the node reports is in its own numbers, so its reservation goes.
func (c *Conns) Report(node *agentrpc.NodeRegisterRequest, session *agentrpc.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, h := range node.GetHeld() {
		delete(c.reserved, h.GetExecutionId())
	}

	name := node.GetName()

	// Replaced, not modified, since readers hold the old entry unlocked.
	if old, ok := c.conns[name]; ok && old.Session == session {
		updated := *old
		updated.Node = node
		c.conns[name] = &updated

		return
	}

	if old, ok := c.conns[name]; ok {
		_ = old.Session.Close()
	}

	c.conns[name] = &Conn{
		Node:        node,
		Executions:  agentrpc.NewAgentExecutionsClient(session.Peer()),
		Address:     session.RemoteAddr().String(),
		Established: c.now(),
		Session:     session,
	}
}

// Remove forgets the nodes connected on session, returning their names.
func (c *Conns) Remove(session *agentrpc.Session) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var removed []string

	for name, conn := range c.conns {
		// A node already reconnected on a new session outlives its old one.
		if conn.Session == session {
			delete(c.conns, name)
			removed = append(removed, name)
		}
	}

	return removed
}

// CloseAll ends every connection, when the server stops accepting agents.
func (c *Conns) CloseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, conn := range c.conns {
		_ = conn.Session.Close()
	}
}

// -------------------------------------------------------------------------
// ROOM
// -------------------------------------------------------------------------

// Reserve holds need on node for execution id, or says why it cannot: the
// node is not connected, or has less room left. Checking and holding are one
// step under the lock, so two placements cannot take the same last room.
func (c *Conns) Reserve(node, id string, need *agentrpc.Resources) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, ok := c.conns[node]
	if !ok {
		return fmt.Errorf("node %s is not connected", node)
	}

	cpu, memory := c.free(conn)
	if need.GetCpu() > cpu || need.GetMemory() > memory {
		return fmt.Errorf("node %s has %d millicores and %d MiB free; the task needs %d and %d",
			node, max(cpu, 0), max(memory, 0), need.GetCpu(), need.GetMemory())
	}

	c.reserved[id] = reservation{node: node, need: need, at: c.now()}

	return nil
}

// Unreserve gives back the room held for id.
func (c *Conns) Unreserve(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.reserved, id)
}

// Free is the room node has left, in millicores and MiB, counting what is
// reserved there; none when it is not connected. Read from its latest report
// under the lock, as a report drops the reservations it accounts for.
// Reservations can take it below zero.
func (c *Conns) Free(node string) (cpu, memory int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, ok := c.conns[node]
	if !ok {
		return 0, 0
	}

	return c.free(conn)
}

// free is Free with mu held. A reservation past its TTL is dropped here.
func (c *Conns) free(conn *Conn) (cpu, memory int64) {
	pending := make(map[string]*agentrpc.Resources)

	for id, r := range c.reserved {
		switch {
		case c.now().Sub(r.at) > reservationTTL:
			delete(c.reserved, id)
		case r.node == conn.Node.GetName():
			pending[id] = r.need
		}
	}

	return agentrpc.Free(conn.Node.GetCapacity(), conn.Node.GetHeld(), pending)
}

// -------------------------------------------------------------------------
// READS
// -------------------------------------------------------------------------

// Get returns the connected node named name.
func (c *Conns) Get(name string) (*Conn, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, ok := c.conns[name]

	return conn, ok
}

// All returns every connected node, by name.
func (c *Conns) All() []*Conn {
	return c.filter(func(*Conn) bool { return true })
}

// InPool returns the connected nodes that joined pool, by name.
func (c *Conns) InPool(pool string) []*Conn {
	return c.filter(func(conn *Conn) bool { return conn.Node.GetPool() == pool })
}

// filter returns the connections keep accepts, sorted by name.
func (c *Conns) filter(keep func(*Conn) bool) []*Conn {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]*Conn, 0, len(c.conns))

	for _, conn := range c.conns {
		if keep(conn) {
			out = append(out, conn)
		}
	}

	slices.SortFunc(out, func(a, b *Conn) int { return strings.Compare(a.Node.GetName(), b.Node.GetName()) })

	return out
}
