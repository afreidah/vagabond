// -------------------------------------------------------------------------------
// Dispatch Leases
//
// Author: Alex Freidah
//
// A dispatch is leased to the process running it, which renews the lease until
// every execution has finished through release. A process that dies stops
// renewing, and a server claims the dispatch once the lease lapses. Renewal
// also carries a cancel requested by a server that does not hold it.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
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

// ErrLeaseLost reports work stopped because another process claimed its
// dispatch.
var ErrLeaseLost = errors.New("dispatch was taken over by another process")

// ErrCancelRequested reports work stopped because a cancel was requested for
// its dispatch.
var ErrCancelRequested = errors.New("the dispatch was cancelled")

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

// lease is this process's hold on one dispatch: the store lease it writes
// under, the renewal running beside it, and the executions working under it.
type lease struct {
	id   execution.ID
	key  execution.Lease
	stop context.CancelFunc
	done chan struct{}

	lost      atomic.Bool
	cancelled chan struct{}
	cancel    sync.Once
	runners   sync.WaitGroup
}

// hold starts renewing key on dispatch id. Renewal runs on its own context so
// a cancelled run keeps its lease until its executions are done.
func (d *Dispatcher) hold(ctx context.Context, id execution.ID, key execution.Lease) *lease {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	l := &lease{id: id, key: key, stop: cancel, done: make(chan struct{}), cancelled: make(chan struct{})}

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

			requested, err := d.renew(ctx, l)

			// Any failure but a lost lease is left for the next tick: a lease has room for two misses
			if errors.Is(err, execution.ErrStale) {
				l.lose()

				return
			}

			if !requested.IsZero() {
				l.requestCancel()
			}
		}
	}()

	return l
}

// renew extends the lease once and returns any cancel requested since it was
// taken.
func (d *Dispatcher) renew(ctx context.Context, l *lease) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, recordTimeout)
	defer cancel()

	return d.executions.RenewDispatch(ctx, l.id, l.key, d.now().Add(d.leaseTTL))
}

// lose records that the store refused a write under the lease, which means
// another process claimed the dispatch, and stops everything working under it.
func (l *lease) lose() {
	l.lost.Store(true)
	l.requestCancel()
}

// requestCancel tells everything working under the lease to stop. Safe to
// call more than once.
func (l *lease) requestCancel() {
	l.cancel.Do(func() { close(l.cancelled) })
}

// stopped is closed once the dispatch is cancelled or its lease is lost.
func (l *lease) stopped() <-chan struct{} {
	return l.cancelled
}

// stopReason is why work under the lease stopped, nil while it has not.
func (l *lease) stopReason() error {
	select {
	case <-l.stopped():
	default:
		return nil
	}

	if l.taken() {
		return ErrLeaseLost
	}

	return ErrCancelRequested
}

// release stops renewing and waits for the renewal goroutine to exit.
func (l *lease) release() {
	l.stop()
	<-l.done
}

// taken reports whether another process has claimed the dispatch, as a
// refused write or renewal showed.
func (l *lease) taken() bool {
	return l.lost.Load()
}
