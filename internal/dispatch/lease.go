// -------------------------------------------------------------------------------
// Dispatch Leases
//
// Author: Alex Freidah
//
// A running dispatch is leased to the process running it, which renews the
// lease until the run ends. A process that dies stops renewing, and a server
// takes the dispatch over once the lease lapses. An owner that finds its lease
// taken stops starting tasks and leaves the ending to the new owner.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Leases run for LeaseTTL and are renewed every LeaseRenew by default, so two
// renewals can fail before another server may take the dispatch over.
const (
	LeaseTTL   = 60 * time.Second
	LeaseRenew = 20 * time.Second
)

// ErrLeaseLost reports a run stopped because another process took its
// dispatch over.
var ErrLeaseLost = errors.New("dispatch was taken over by another process")

// -------------------------------------------------------------------------
// OWNERS
// -------------------------------------------------------------------------

// ProcessOwner names this process as a dispatch owner: its role, host and
// process ID. Read by people, not compared for anything but equality.
func ProcessOwner(role string) string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}

	return fmt.Sprintf("%s:%s:%d", role, host, os.Getpid())
}

// -------------------------------------------------------------------------
// LEASE
// -------------------------------------------------------------------------

// lease renews one dispatch's lease in the background until released. lost is
// set when a renewal finds the dispatch held by someone else or ended.
type lease struct {
	stop context.CancelFunc
	done chan struct{}
	lost atomic.Bool
}

// hold starts renewing the lease on dispatch id. Renewal runs on its own
// context so a cancelled run keeps its lease until it has recorded its end.
func (d *Dispatcher) hold(ctx context.Context, id execution.ID) *lease {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	l := &lease{stop: cancel, done: make(chan struct{})}

	go func() {
		defer close(l.done)

		ticker := time.NewTicker(d.leaseRenew)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			if errors.Is(d.renew(ctx, id), execution.ErrStale) {
				l.lost.Store(true)

				return
			}
		}
	}()

	return l
}

// renew extends the lease once. Any failure other than a stale lease is left
// for the next tick, since a lease has room for two misses.
func (d *Dispatcher) renew(ctx context.Context, id execution.ID) error {
	ctx, cancel := context.WithTimeout(ctx, recordTimeout)
	defer cancel()

	return d.executions.RenewDispatch(ctx, id, d.owner, d.now().Add(d.leaseTTL))
}

// release stops renewing and waits for the renewal goroutine to exit. Safe on
// a nil lease.
func (l *lease) release() {
	if l == nil {
		return
	}

	l.stop()
	<-l.done
}

// taken reports whether another process has taken the dispatch over. False on
// a nil lease.
func (l *lease) taken() bool {
	return l != nil && l.lost.Load()
}
