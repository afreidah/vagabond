// -------------------------------------------------------------------------------
// Provider - the plugin boundary
//
// Author: Alex Freidah
//
// Every cloud backend implements this, and the dispatcher is written against it
// and nothing else. A plugin is translation plus classification: it turns a
// normalized task into whatever its platform requires, and turns that
// platform's failures into a classified error. Scheduling, retry, quota
// accounting, and provider selection all stay in the control plane.
//
// This is the one producer-declared interface in the tree. Everywhere else a
// consumer declares the narrow interface it needs, but plugin authors write
// against this without being able to see the consumers, so it is declared once,
// here, and implementations are written to it rather than the reverse.
// -------------------------------------------------------------------------------

package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// INTERFACE
// -------------------------------------------------------------------------

// Provider is one cloud backend Vagabond can dispatch to.
//
// Implementations must not retry, back off, or choose an alternative provider.
// Those are control plane decisions that need a view of every provider and of
// the free-tier ledger, neither of which a plugin has. A plugin that retries
// internally spends quota the ledger never sees.
//
// Every returned error should be a *Error so that the dispatcher can tell an
// infrastructure failure from Vagabond's own bug. ClassifyHTTP covers the usual
// shape.
type Provider interface {
	// Name is the routing identifier a job's provider list refers to, such as
	// "ibm-code-engine". It is stable for the life of the provider, because it
	// is persisted on every execution and quota row.
	Name() string

	// Capabilities reports what this provider can currently do.
	//
	// Called by a refresh loop rather than on the request path, because
	// admission must decide from a cached snapshot without reaching the
	// network. Implementations may call their platform here.
	Capabilities(ctx context.Context) (Capabilities, error)

	// Submit dispatches a task under an id Vagabond has already recorded.
	//
	// The id is supplied rather than returned so that it exists durably before
	// the call: a crash in between then leaves a row to reconcile against
	// rather than an orphaned run. It is also the submission idempotency key,
	// so a retry with the same id must not start a second run.
	//
	// The task is passed by pointer and must be treated as read-only.
	// Implementations must not modify it: the same task is offered to other
	// candidates when a submission is rerouted, and a plugin that rewrote it
	// would change what every later provider is asked to run. A value would not
	// prevent that anyway, since Task holds maps, slices, and pointers that a
	// copy would share.
	Submit(ctx context.Context, id execution.ID, task *job.Task) (Submission, error)

	// Status reports where a previously submitted execution has reached.
	//
	// Only meaningful for providers whose work outlives the Submit call. A
	// provider that finished inside Submit returns ErrUnsupported.
	Status(ctx context.Context, id execution.ID) (execution.Status, error)

	// Cancel stops a running execution.
	//
	// Returns ErrUnsupported where the platform offers no way to stop work.
	// Cancelling an execution that already finished is not an error: the
	// caller's intent, that it not be running, is satisfied.
	Cancel(ctx context.Context, id execution.ID) error
}

// -------------------------------------------------------------------------
// SUBMISSION
// -------------------------------------------------------------------------

// Submission is what a provider reports back from dispatching a task.
//
// Result is the reason this is a struct rather than a provider id. Two of the
// three execution families finish inside Submit: a function invocation and a
// worker call both return their answer synchronously, while a container job is
// only acknowledged and reports its result later through ingest. Carrying an
// optional result lets one interface describe both without the dispatcher
// having to ask which kind of provider it is holding.
//
// State says which of those happened. A provider that merely queued the work
// reports StateAccepted; one that already finished reports a terminal state and
// fills in Result.
type Submission struct {
	ProviderID string
	State      execution.State
	Result     *execution.Result
}

// Synchronous reports whether the work finished inside Submit.
func (s *Submission) Synchronous() bool {
	return s.Result != nil
}

// Validate checks that a provider reported something coherent.
//
// The dispatcher calls this on every submission, because a plugin that reports
// a terminal state with no result, or a result alongside a state that says the
// work is still queued, will otherwise be discovered as a stuck execution hours
// later rather than as a bug at the call site.
func (s *Submission) Validate() error {
	if !s.State.Valid() {
		return fmt.Errorf("%w: submission state %q", ErrInvalidSubmission, s.State)
	}

	if !slices.Contains(submittableStates, s.State) {
		return fmt.Errorf("%w: a submission cannot report state %q", ErrInvalidSubmission, s.State)
	}

	if s.Result != nil && !s.State.Terminal() {
		return fmt.Errorf("%w: state %q carries a result but is not terminal",
			ErrInvalidSubmission, s.State)
	}

	if s.Result == nil && s.State.Terminal() {
		return fmt.Errorf("%w: state %q is terminal but carries no result",
			ErrInvalidSubmission, s.State)
	}

	return nil
}

// submittableStates are the states a provider may report from Submit.
//
// Pending and submitted are Vagabond's own bookkeeping from before the call.
// Lost is something only the control plane concludes. Cancelled is the result
// of a Cancel that has not happened yet. None of them can be the answer to a
// submission.
var submittableStates = []execution.State{
	execution.StateAccepted,
	execution.StateRunning,
	execution.StateSucceeded,
	execution.StateFailed,
}

// -------------------------------------------------------------------------
// UNSUPPORTED OPERATIONS
// -------------------------------------------------------------------------

// ErrUnsupported reports an operation the platform has no equivalent for.
//
// Distinct from a failure: nothing went wrong, and retrying or rerouting will
// not help. A worker invocation cannot be cancelled mid-request because there
// is nothing to cancel, not because Cloudflare was unreachable.
var ErrUnsupported = errors.New("operation not supported by this provider")

// ErrInvalidSubmission reports a provider describing its own submission
// incoherently.
var ErrInvalidSubmission = errors.New("invalid submission")

// StatusNotSupported is embedded by providers whose work finishes inside
// Submit, so that declaring the fact is one line rather than a method body
// every plugin writes slightly differently.
type StatusNotSupported struct{}

// Status reports that this provider has no work outliving Submit to report on.
func (StatusNotSupported) Status(context.Context, execution.ID) (execution.Status, error) {
	return execution.Status{}, Internal(fmt.Errorf("status: %w", ErrUnsupported))
}

// CancelNotSupported is embedded by providers offering no way to stop work.
type CancelNotSupported struct{}

// Cancel reports that this provider cannot stop work once it has started.
func (CancelNotSupported) Cancel(context.Context, execution.ID) error {
	return Internal(fmt.Errorf("cancel: %w", ErrUnsupported))
}
