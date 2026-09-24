// -------------------------------------------------------------------------------
// Reaper Tests
//
// Author: Alex Freidah
//
// What each provider answer becomes. The ledger's own tests cover what each
// verdict does to the counters.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/plugin"
)

// statusProvider answers Status with whatever a case gives it.
type statusProvider struct {
	*scriptedProvider
	status execution.Status
	err    error
}

func (p *statusProvider) Status(context.Context, execution.ID) (execution.Status, error) {
	return p.status, p.err
}

func TestResolve(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	ended := started.Add(90 * time.Second)

	tests := []struct {
		name         string
		status       execution.Status
		err          error
		want         ledger.Verdict
		wantRan      time.Duration
		unregistered bool
	}{
		{
			name: "never heard of it", want: ledger.Drop,
			err: plugin.Internal(fmt.Errorf("%w: gone", plugin.ErrUnknownExecution)),
		},
		{
			name: "finished inside Submit", want: ledger.Stand,
			err: plugin.Internal(fmt.Errorf("status: %w", plugin.ErrUnsupported)),
		},
		{
			name: "could not be asked", want: ledger.Keep,
			err: plugin.Infrastructure(errors.New("503")),
		},
		{
			name: "still running", want: ledger.Keep,
			status: execution.Status{State: execution.StateRunning, StartedAt: started},
		},
		{
			name: "finished", want: ledger.Finished, wantRan: 90 * time.Second,
			status: execution.Status{State: execution.StateSucceeded, StartedAt: started, EndedAt: ended},
		},
		{
			name: "ended without starting", want: ledger.Finished,
			status: execution.Status{State: execution.StateCancelled, EndedAt: ended},
		},
		{
			name: "started with no recorded end", want: ledger.Stand,
			status: execution.Status{State: execution.StateFailed, StartedAt: started},
		},
		{
			// Lost is not terminal: reconciliation may still learn the answer.
			name: "lost", want: ledger.Keep,
			status: execution.Status{State: execution.StateLost, StartedAt: started},
		},
		{
			name: "provider no longer configured", want: ledger.Keep, unregistered: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := newRegistry()
			if !tt.unregistered {
				reg.providers["a"] = &statusProvider{
					scriptedProvider: &scriptedProvider{name: "a"},
					status:           tt.status,
					err:              tt.err,
				}
			}

			id, err := execution.NewID()
			if err != nil {
				t.Fatalf("NewID() = %v", err)
			}

			verdict, ran := newDispatcher(t, reg).resolve(t.Context(), ledger.Held{ID: id, Provider: "a"})

			if verdict != tt.want || ran != tt.wantRan {
				t.Errorf("resolve() = %d, %v; want %d, %v", verdict, ran, tt.want, tt.wantRan)
			}
		})
	}
}
