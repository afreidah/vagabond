//go:build integration

// -------------------------------------------------------------------------------
// Cuttable Connection Proxy
//
// Author: Alex Freidah
//
// A TCP proxy between a store and its database that a test can cut and
// restore, to stage an outage. Cutting closes the listener and every open
// connection, so the store sees refused connections and broken ones, as it
// would when the database goes away; restoring listens on the same address
// again.
// -------------------------------------------------------------------------------

package pgtest

import (
	"context"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"

	"github.com/afreidah/vagabond/internal/state/postgres"
)

// Proxy forwards connections on its own address to an engine's database.
type Proxy struct {
	dsn    string // the engine's DSN, pointed at the proxy
	target string
	addr   string

	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
}

// NewProxy starts a proxy in front of e's database, cut when the test ends.
func NewProxy(t *testing.T, e *Engine) *Proxy {
	t.Helper()

	u, err := url.Parse(e.ready(t))
	if err != nil {
		t.Fatalf("%s: parsing the DSN: %v", e.Name, err)
	}

	p := &Proxy{target: u.Host, conns: make(map[net.Conn]struct{})}

	p.listen(t, "127.0.0.1:0")
	p.addr = p.listener.Addr().String()

	u.Host = p.addr
	p.dsn = u.String()

	t.Cleanup(p.Cut)

	return p
}

// Open returns a store connected through the proxy, with every table emptied,
// closed when the test ends.
func (p *Proxy) Open(t *testing.T, e *Engine) *postgres.Store {
	t.Helper()

	ctx := context.Background()

	if err := empty(ctx, e.ready(t)); err != nil {
		t.Fatalf("%s: emptying tables: %v", e.Name, err)
	}

	store, err := postgres.Open(ctx, p.dsn)
	if err != nil {
		t.Fatalf("%s: open through the proxy: %v", e.Name, err)
	}

	t.Cleanup(store.Close)

	return store
}

// Cut stops accepting and closes every open connection.
func (p *Proxy) Cut() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.listener != nil {
		_ = p.listener.Close()
		p.listener = nil
	}

	for conn := range p.conns {
		_ = conn.Close()
		delete(p.conns, conn)
	}
}

// Restore listens on the proxy's address again.
func (p *Proxy) Restore(t *testing.T) {
	t.Helper()

	p.listen(t, p.addr)
}

// listen starts accepting on addr.
func (p *Proxy) listen(t *testing.T, addr string) {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("proxy listen on %s: %v", addr, err)
	}

	p.mu.Lock()
	p.listener = listener
	p.mu.Unlock()

	go p.accept(listener)
}

// accept forwards each connection until the listener is closed.
func (p *Proxy) accept(listener net.Listener) {
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}

		var dialer net.Dialer

		upstream, err := dialer.DialContext(context.Background(), "tcp", p.target)
		if err != nil {
			_ = client.Close()

			continue
		}

		p.track(client, upstream)

		go p.pipe(client, upstream)
		go p.pipe(upstream, client)
	}
}

// track records open connections so Cut can close them.
func (p *Proxy) track(conns ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, conn := range conns {
		p.conns[conn] = struct{}{}
	}
}

// pipe copies one direction, closing both ends when either does.
func (p *Proxy) pipe(from, to net.Conn) {
	_, _ = io.Copy(to, from)

	_ = from.Close()
	_ = to.Close()

	p.mu.Lock()
	delete(p.conns, from)
	delete(p.conns, to)
	p.mu.Unlock()
}
