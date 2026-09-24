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
	"io"
	"slices"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// INTERFACE
// -------------------------------------------------------------------------

// Provider is one cloud backend Vagabond can dispatch to.
//
// Plugins translate and classify, nothing more: no retrying, no backoff, no
// choosing another provider. Errors should be *Error. The id is the
// idempotency key; the task is read-only. Cancel may destroy what Result reads,
// so callers fetch the result first.
type Provider interface {
	Name() string
	Capabilities(ctx context.Context) (Capabilities, error)
	Submit(ctx context.Context, id execution.ID, task *job.Task) (Submission, error)
	Status(ctx context.Context, id execution.ID) (execution.Status, error)
	Result(ctx context.Context, id execution.ID) (*execution.Result, error)
	Cancel(ctx context.Context, id execution.ID) error
}

// -------------------------------------------------------------------------
// SUBMISSION
// -------------------------------------------------------------------------

// Submission is what a provider reports back from dispatching a task.
//
// Result carries the answer for providers that finish inside Submit, so one
// interface describes both families. A provider that merely queued the work
// reports StateAccepted and leaves it nil.
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
// OPTIONAL CAPABILITIES
// -------------------------------------------------------------------------

// LogStreamer is implemented by providers that can show a running execution's
// output before it finishes. A live view, never the record: Result stays
// authoritative, and a broken stream is not a failed execution.
type LogStreamer interface {
	StreamLogs(ctx context.Context, id execution.ID, w io.Writer) error
}

// Releaser is implemented by providers that leave a resource behind, as Cloud
// Run does. Called after Result, best effort, never failing the execution.
type Releaser interface {
	Release(ctx context.Context, id execution.ID) error
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

// ErrUnknownExecution reports an execution the platform has no record of. The
// reaper reads it as never having run, so it drops the quota reservation. Wrap
// it only when the platform says so, never for a lookup that merely failed.
var ErrUnknownExecution = errors.New("unknown execution")

// StatusNotSupported is embedded by providers whose work finishes inside
// Submit, so that declaring the fact is one line rather than a method body
// every plugin writes slightly differently.
type StatusNotSupported struct{}

// Status reports that this provider has no work outliving Submit to report on.
func (StatusNotSupported) Status(context.Context, execution.ID) (execution.Status, error) {
	return execution.Status{}, Internal(fmt.Errorf("status: %w", ErrUnsupported))
}

// ResultNotSupported is embedded by providers that returned everything from
// Submit, so there is nothing left to fetch.
type ResultNotSupported struct{}

// Result reports that this provider already returned what the execution
// produced.
func (ResultNotSupported) Result(context.Context, execution.ID) (*execution.Result, error) {
	return nil, Internal(fmt.Errorf("result: %w", ErrUnsupported))
}

// CancelNotSupported is embedded by providers offering no way to stop work.
type CancelNotSupported struct{}

// Cancel reports that this provider cannot stop work once it has started.
func (CancelNotSupported) Cancel(context.Context, execution.ID) error {
	return Internal(fmt.Errorf("cancel: %w", ErrUnsupported))
}
