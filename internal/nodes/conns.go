// -------------------------------------------------------------------------------
// Node Connections
//
// Author: Alex Freidah
//
// Every agent node connected to this server, by name, with what it last
// reported and a client for calling its executions. The server adds and
// removes nodes as agents connect and drop; pool providers read them to know
// what they can run and where. When a node leaves, the time is kept, so a pool
// can tell a node that just blipped from one that is gone.
// -------------------------------------------------------------------------------

package nodes

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/agentrpc"
)

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

// Used returns what the node's running workloads declared, in millicores and
// MiB, and how many there are, as the agent last reported them.
func (c *Conn) Used() (cpu, memory int64, running int) {
	for _, h := range c.Node.GetHeld() {
		// A finished workload keeps its container until released, but no
		// longer uses the node.
		if h.GetRunning() {
			cpu += h.GetResources().GetCpu()
			memory += h.GetResources().GetMemory()
			running++
		}
	}

	return cpu, memory, running
}

// Conns is every connected node by name, and when each departed node left.
type Conns struct {
	now func() time.Time

	mu    sync.Mutex
	conns map[string]*Conn
	left  map[string]time.Time
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New returns an empty set of connections.
func New() *Conns {
	return &Conns{now: time.Now, conns: make(map[string]*Conn), left: make(map[string]time.Time)}
}

// -------------------------------------------------------------------------
// CHANGES
// -------------------------------------------------------------------------

// Report records what a node reported on session. A first report adds the
// node; a later one on the same session updates it; one on a new session is
// the agent reconnecting, and replaces and closes the old connection.
func (c *Conns) Report(node *agentrpc.NodeRegisterRequest, session *agentrpc.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()

	name := node.GetName()
	delete(c.left, name)

	// The same connection reporting again: only what it reported changes. The
	// entry is replaced, not modified, since readers hold the old one unlocked.
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

// Remove forgets the nodes connected on session and records when they left,
// returning their names.
func (c *Conns) Remove(session *agentrpc.Session) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var removed []string

	for name, conn := range c.conns {
		// A node that already reconnected on a new session is not removed
		// when its old session closes.
		if conn.Session == session {
			delete(c.conns, name)
			c.left[name] = c.now()
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

// LeftAt reports when a node that is no longer connected left.
func (c *Conns) LeftAt(name string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	at, ok := c.left[name]

	return at, ok
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
