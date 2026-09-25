// -------------------------------------------------------------------------------
// Execution Records
//
// Author: Alex Freidah
//
// Every state an attempt reaches is written to the execution store as it
// happens. Creating the record is required, since it must exist before Submit;
// every write after is best effort. The run already happened, and failing it
// over a record would lose the answer the record exists to keep.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// recordTimeout bounds one write, on its own context so an interrupted run is
// still recorded.
const recordTimeout = 10 * time.Second

// tracked is one attempt's record and the store it is written to.
type tracked struct {
	rec   execution.Record
	store Executions
	now   func() time.Time
}

// to moves the record to next and writes it. A move the state machine refuses,
// or to the state it is already in, writes nothing.
func (t *tracked) to(ctx context.Context, next execution.State) {
	from := t.rec.State
	if from == next {
		return
	}

	if err := t.rec.To(next, t.now()); err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()

	_ = t.store.Update(ctx, &t.rec, from)
}

// submitted records the provider's acceptance and the state it reported.
func (t *tracked) submitted(ctx context.Context, sub *plugin.Submission) {
	t.rec.ProviderID = sub.ProviderID
	t.to(ctx, execution.StateSubmitted)

	if sub.Result != nil {
		t.finish(ctx, sub.State, sub.Result)

		return
	}

	t.to(ctx, sub.State)
}

// finish records a terminal state and what the execution produced, which may
// be nothing when the result could not be fetched.
func (t *tracked) finish(ctx context.Context, state execution.State, result *execution.Result) {
	t.rec.Result = result.Bounded()
	t.to(ctx, state)
}

// failed records an attempt that ended without an answer. Before submission it
// failed outright; after, nothing more is known and it is lost.
func (t *tracked) failed(ctx context.Context, err error) {
	t.rec.Failure = string(plugin.ClassInternal)

	var classified *plugin.Error
	if errors.As(err, &classified) {
		t.rec.Failure = string(classified.Class)
	}

	if t.rec.State == execution.StatePending {
		t.to(ctx, execution.StateFailed)

		return
	}

	t.to(ctx, execution.StateLost)
}
