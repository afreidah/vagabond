// -------------------------------------------------------------------------------
// Provider - the plugin boundary
//
// Author: Alex Freidah
//
// Every cloud backend implements this, and the dispatcher is written against it
// and nothing else. A plugin is translation plus classification; scheduling,
// retry, quota accounting, and provider selection stay in the control plane.
// This is the one producer-declared interface in the tree, because plugin
// authors write against it without seeing the consumers.
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
// idempotency key; the task is read-only. Cancel may destroy what Result and
// ReadLogs read, so callers fetch the result and output first.
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

// Validate checks that a provider reported something coherent, so a plugin bug
// surfaces at the call site rather than as a stuck execution hours later.
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

// submittableStates are the states a provider may report from Submit. The rest
// are the control plane's own bookkeeping or conclusions.
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
// output before it finishes. A live view, never the record: the stored copy
// stays authoritative, and a broken stream is not a failed execution.
//
// A stream starts at the first byte the task printed, whenever it opens, and
// offset is how many bytes the caller already has. One that cannot be placed
// there returns ErrNoResume, before writing anything when it can.
type LogStreamer interface {
	StreamLogs(ctx context.Context, id execution.ID, offset int64, w io.Writer) error
}

// LogReader is implemented by providers that hold a finished execution's
// output, read once it has ended and before Release, which may destroy it.
// ReadLogs writes the whole output to w; an error means what was written is
// incomplete.
type LogReader interface {
	ReadLogs(ctx context.Context, id execution.ID, w io.Writer) error
}

// Live is implemented by providers whose capabilities change between
// refreshes and cost nothing to read, as agent nodes' do as they join and fill.
// The registry reads them on every plan instead of from the last refresh; an
// error marks the provider unhealthy.
type Live interface {
	LiveCapabilities() (Capabilities, error)
}

// Nodes is implemented by providers made of agent nodes, each of which
// admission judges and ranking places as a candidate of its own. On returns
// the provider for the executions on one node.
type Nodes interface {
	On(node string) Provider
}

// Reserver is implemented by a node's provider, which holds room on the node
// for an execution before it is recorded. A node with too little left is
// ErrNoRoom, which tries the next candidate without spending an attempt.
type Reserver interface {
	Reserve(id execution.ID, task *job.Task) error
}

// On returns the provider for executions on node: the node's own for a
// provider made of nodes, p itself otherwise.
func On(p Provider, node string) Provider {
	if nodes, ok := p.(Nodes); ok {
		return nodes.On(node)
	}

	return p
}

// Releaser is implemented by providers that leave a resource behind, as Cloud
// Run does. Called after Result, best effort, never failing the execution.
type Releaser interface {
	Release(ctx context.Context, id execution.ID) error
}

// Recredentialer is implemented by providers that hold a credential. After a
// call fails with ErrUnauthorized, the credential is resolved again from its
// source and handed over, and the provider rebuilds whatever signs its
// requests from it.
type Recredentialer interface {
	Recredential(ctx context.Context, credential []byte) error
}

// -------------------------------------------------------------------------
// UNSUPPORTED OPERATIONS
// -------------------------------------------------------------------------

// ErrUnsupported reports an operation the platform has no equivalent for.
// Nothing went wrong, so retrying or rerouting does not help.
var ErrUnsupported = errors.New("operation not supported by this provider")

// ErrInvalidSubmission reports a provider describing its own submission
// incoherently.
var ErrInvalidSubmission = errors.New("invalid submission")

// ErrUnknownExecution reports an execution the platform has no record of:
// deleted, or never received. One being watched fails at once, one stopped
// before it was seen running is charged nothing, and a Release given it has
// nothing left to free. Wrap it only when the platform says so, never for a
// lookup that merely failed.
var ErrUnknownExecution = errors.New("unknown execution")

// ErrNoRoom reports a node with too little room left for a task, so nothing
// was placed on it.
var ErrNoRoom = errors.New("no room on the node")

// ErrNoResume reports a log stream that cannot be placed at an offset, so a
// caller resuming one finishes from the stored output instead.
var ErrNoResume = errors.New("this provider's log stream cannot resume from an offset")

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
