// -------------------------------------------------------------------------------
// Provider Interface Tests
//
// Author: Alex Freidah
//
// The compile-time assertions below are the point of this file: the interface
// has to be satisfiable by all three execution families without any of them
// contorting, and a family that needed an unimplemented panic to compile would
// mean the boundary is drawn wrong.
// -------------------------------------------------------------------------------

package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ptr"
)

// All three execution families satisfy one interface. This is the assertion
// that fails first if a future method cannot be honoured by one of them.
var (
	_ Provider = (*FakeContainerProvider)(nil)
	_ Provider = (*FakeSyncProvider)(nil)
)

func newID(t *testing.T) execution.ID {
	t.Helper()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() returned unexpected error: %v", err)
	}

	return id
}

// -------------------------------------------------------------------------
// ASYNCHRONOUS FAMILY
// -------------------------------------------------------------------------

// A container provider acknowledges work and reports no result, because the
// result arrives later through ingest.
func TestProvider_ContainerSubmitIsAsynchronous(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")
	id := newID(t)

	sub, err := p.Submit(t.Context(), id, &job.Task{})
	if err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	if err := sub.Validate(); err != nil {
		t.Fatalf("Submit produced an invalid submission: %v", err)
	}

	if sub.Synchronous() {
		t.Error("a container submission reports as synchronous")
	}

	if sub.State != execution.StateAccepted {
		t.Errorf("State = %q, want %q", sub.State, execution.StateAccepted)
	}

	if sub.ProviderID == "" {
		t.Error("the provider reported no identifier of its own")
	}
}

func TestProvider_ContainerStatusTracksTheRun(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")
	id := newID(t)

	if _, err := p.Submit(t.Context(), id, &job.Task{}); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	if err := p.Advance(id, execution.StateRunning); err != nil {
		t.Fatalf("Advance returned unexpected error: %v", err)
	}

	status, err := p.Status(t.Context(), id)
	if err != nil {
		t.Fatalf("Status returned unexpected error: %v", err)
	}

	if status.State != execution.StateRunning {
		t.Errorf("State = %q, want %q", status.State, execution.StateRunning)
	}
}

// Cancelling something that already finished is not an error. The caller wanted
// it not running, and it is not.
func TestProvider_CancelAfterTerminalIsNotAnError(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")
	id := newID(t)

	if _, err := p.Submit(t.Context(), id, &job.Task{}); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	if err := p.Advance(id, execution.StateSucceeded); err != nil {
		t.Fatalf("Advance returned unexpected error: %v", err)
	}

	if err := p.Cancel(t.Context(), id); err != nil {
		t.Errorf("cancelling a finished execution returned %v, want nil", err)
	}
}

// -------------------------------------------------------------------------
// SYNCHRONOUS FAMILY
// -------------------------------------------------------------------------

// A function provider finishes inside Submit, which is the whole reason
// Submission carries an optional result.
func TestProvider_FunctionSubmitIsSynchronous(t *testing.T) {
	p := NewFakeFunctionProvider("fake-function")
	id := newID(t)

	sub, err := p.Submit(t.Context(), id, &job.Task{})
	if err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	if err := sub.Validate(); err != nil {
		t.Fatalf("Submit produced an invalid submission: %v", err)
	}

	if !sub.Synchronous() {
		t.Fatal("a function submission does not report as synchronous")
	}

	if !sub.State.Terminal() {
		t.Errorf("State = %q, want a terminal state", sub.State)
	}

	if sub.Result.ID != id {
		t.Errorf("Result.ID = %s, want %s", sub.Result.ID, id)
	}
}

// A worker has no process to exit, so its result carries no exit code and still
// reports success.
func TestProvider_WorkerResultHasNoExitCode(t *testing.T) {
	p := NewFakeWorkerProvider("fake-worker")

	sub, err := p.Submit(t.Context(), newID(t), &job.Task{})
	if err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	if sub.Result.ExitCode != nil {
		t.Errorf("a worker result carries exit code %d", *sub.Result.ExitCode)
	}

	if sub.State != execution.StateSucceeded {
		t.Errorf("State = %q, want %q", sub.State, execution.StateSucceeded)
	}
}

func TestProvider_NonZeroExitFailsTheSubmission(t *testing.T) {
	p := NewFakeFunctionProvider("fake-function")
	p.ExitCode = ptr.Of(1)

	sub, err := p.Submit(t.Context(), newID(t), &job.Task{})
	if err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	if sub.State != execution.StateFailed {
		t.Errorf("State = %q, want %q", sub.State, execution.StateFailed)
	}

	// A failing build is still a successful submission. The error return stays
	// nil, because nothing went wrong with Vagabond or the provider.
	if err != nil {
		t.Error("a non-zero exit produced a Go error")
	}
}

// -------------------------------------------------------------------------
// UNSUPPORTED OPERATIONS
// -------------------------------------------------------------------------

// The embedded helpers are the demonstration: a provider with no work outliving
// Submit declares that in two lines instead of two method bodies.
func TestProvider_SyncProviderRejectsStatusResultAndCancel(t *testing.T) {
	p := NewFakeWorkerProvider("fake-worker")
	id := newID(t)

	_, err := p.Status(t.Context(), id)
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Status error = %v, want ErrUnsupported", err)
	}

	// Nothing left to fetch: the result came back from Submit.
	if _, err := p.Result(t.Context(), id); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Result error = %v, want ErrUnsupported", err)
	}

	if err := p.Cancel(t.Context(), id); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Cancel error = %v, want ErrUnsupported", err)
	}
}

// -------------------------------------------------------------------------
// FETCHING A RESULT
// -------------------------------------------------------------------------

// A container provider's result has to be asked for, which is the whole reason
// the method exists: the platform holds it and the submission did not.
func TestProvider_ContainerResultIsFetched(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")
	id := newID(t)

	if _, err := p.Submit(t.Context(), id, &job.Task{Name: "test"}); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	result, err := p.Result(t.Context(), id)
	if err != nil {
		t.Fatalf("Result returned unexpected error: %v", err)
	}

	if result.ID != id {
		t.Errorf("result ID = %s, want %s", result.ID, id)
	}

	if !result.Succeeded() {
		t.Error("a zero exit code did not report success")
	}

	if len(result.Logs) == 0 {
		t.Error("the result carries no output")
	}
}

// A workload failing is an answer, not an error: Result returns it normally and
// the exit code is what says the task failed.
func TestProvider_ContainerResultCarriesNonZeroExit(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")
	p.ExitCode = 3

	id := newID(t)

	if _, err := p.Submit(t.Context(), id, &job.Task{Name: "test"}); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	result, err := p.Result(t.Context(), id)
	if err != nil {
		t.Fatalf("a failing workload made Result an error: %v", err)
	}

	if ptr.Deref(result.ExitCode) != 3 {
		t.Errorf("exit code = %d, want 3", ptr.Deref(result.ExitCode))
	}

	if result.Succeeded() {
		t.Error("exit code 3 reported success")
	}
}

// Asking about an execution the provider never saw is Vagabond's own bug, so it
// classifies as internal and must not be rerouted.
func TestProvider_ResultForUnknownExecution(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")

	_, err := p.Result(t.Context(), newID(t))
	if err == nil {
		t.Fatal("expected an error for an unknown execution")
	}

	var classified *Error
	if !errors.As(err, &classified) {
		t.Fatalf("errors.As did not yield *Error, got %T", err)
	}

	if classified.Class != ClassInternal {
		t.Errorf("Class = %q, want %q", classified.Class, ClassInternal)
	}
}

// Unsupported is Vagabond's own fault for asking, so it is internal and must
// never be rerouted: another provider will not answer differently.
func TestProvider_UnsupportedIsInternalAndNotReroutable(t *testing.T) {
	p := NewFakeWorkerProvider("fake-worker")

	err := p.Cancel(t.Context(), newID(t))

	var classified *Error
	if !errors.As(err, &classified) {
		t.Fatalf("errors.As did not yield *Error, got %T", err)
	}

	if classified.Class != ClassInternal {
		t.Errorf("Class = %q, want %q", classified.Class, ClassInternal)
	}

	if classified.Reroutable() {
		t.Error("an unsupported operation reports as reroutable")
	}
}

// -------------------------------------------------------------------------
// SUBMISSION VALIDATION
// -------------------------------------------------------------------------

func TestSubmission_Validate(t *testing.T) {
	tests := []struct {
		name  string
		input Submission
		valid bool
	}{
		{
			name:  "acknowledged work",
			input: Submission{State: execution.StateAccepted},
			valid: true,
		},
		{
			name:  "already running",
			input: Submission{State: execution.StateRunning},
			valid: true,
		},
		{
			name:  "finished with a result",
			input: Submission{State: execution.StateSucceeded, Result: &execution.Result{}},
			valid: true,
		},
		{
			name:  "terminal with no result",
			input: Submission{State: execution.StateSucceeded},
			valid: false,
		},
		{
			name:  "result alongside queued work",
			input: Submission{State: execution.StateAccepted, Result: &execution.Result{}},
			valid: false,
		},
		{
			name:  "reporting our own bookkeeping back at us",
			input: Submission{State: execution.StatePending},
			valid: false,
		},
		{
			name:  "concluding an execution is lost",
			input: Submission{State: execution.StateLost},
			valid: false,
		},
		{
			name:  "no state at all",
			input: Submission{},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.Validate()

			if tt.valid && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}

			if !tt.valid && err == nil {
				t.Error("Validate() = nil, want an error")
			}

			if !tt.valid && err != nil && !errors.Is(err, ErrInvalidSubmission) {
				t.Errorf("error does not wrap ErrInvalidSubmission: %v", err)
			}
		})
	}
}

// -------------------------------------------------------------------------
// CAPABILITIES
// -------------------------------------------------------------------------

// Capabilities hands out a clone, so a caller that sorted the driver list could
// not reorder it for every other holder of that snapshot.
func TestProvider_CapabilitiesAreCloned(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")

	first, err := p.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities returned unexpected error: %v", err)
	}

	if len(first.Drivers) == 0 {
		t.Fatal("the fake advertises no drivers")
	}

	first.Drivers[0] = "mutated"

	second, err := p.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities returned unexpected error: %v", err)
	}

	if second.Drivers[0] == "mutated" {
		t.Error("mutating a returned snapshot changed what the provider reports")
	}
}

// -------------------------------------------------------------------------
// FAKE PROVIDER BEHAVIOUR
// -------------------------------------------------------------------------

// The fakes are used by every downstream package's tests, so their own error
// paths are worth pinning rather than assuming.
func TestFakes_NameAndCapabilities(t *testing.T) {
	container := NewFakeContainerProvider("fake-container")
	sync := NewFakeWorkerProvider("fake-worker")

	if container.Name() != "fake-container" {
		t.Errorf("Name() = %q, want fake-container", container.Name())
	}

	if sync.Name() != "fake-worker" {
		t.Errorf("Name() = %q, want fake-worker", sync.Name())
	}

	caps, err := sync.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities returned unexpected error: %v", err)
	}

	if len(caps.Architectures) != 0 {
		t.Error("the worker fake advertises an architecture")
	}
}

// An operation naming an execution the provider never saw is Vagabond asking
// about something that does not exist, which is our bug rather than theirs.
func TestFakeContainerProvider_UnknownExecution(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")
	id := newID(t)

	if _, err := p.Status(t.Context(), id); !errors.Is(err, ErrUnknownExecution) {
		t.Errorf("Status on an unknown execution = %v, want ErrUnknownExecution", err)
	}

	if err := p.Cancel(t.Context(), id); err == nil {
		t.Error("Cancel on an unknown execution returned no error")
	}

	if err := p.Advance(id, execution.StateRunning); err == nil {
		t.Error("Advance on an unknown execution returned no error")
	}
}

func TestFakeContainerProvider_CancelAndIllegalAdvance(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")
	id := newID(t)

	if _, err := p.Submit(t.Context(), id, &job.Task{}); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	if err := p.Cancel(t.Context(), id); err != nil {
		t.Fatalf("Cancel returned unexpected error: %v", err)
	}

	status, err := p.Status(t.Context(), id)
	if err != nil {
		t.Fatalf("Status returned unexpected error: %v", err)
	}

	if status.State != execution.StateCancelled {
		t.Errorf("State = %q, want %q", status.State, execution.StateCancelled)
	}

	// A cancelled execution is terminal, so reviving it must be refused by the
	// same transition table everything else uses.
	if err := p.Advance(id, execution.StateRunning); err == nil {
		t.Error("advancing a cancelled execution back to running was allowed")
	}
}

func TestFakeSyncProvider_SubmitError(t *testing.T) {
	p := NewFakeFunctionProvider("fake-function")
	p.SubmitErr = Infrastructure(errors.New("throttled"))

	if _, err := p.Submit(t.Context(), newID(t), &job.Task{}); !errors.Is(err, ErrProvider) {
		t.Errorf("error = %v, want a classified provider failure", err)
	}
}

func TestProvider_SubmitErrorIsReturnedUnchanged(t *testing.T) {
	p := NewFakeContainerProvider("fake-container")
	p.SubmitErr = Infrastructure(errors.New("service unavailable"))

	_, err := p.Submit(t.Context(), newID(t), &job.Task{})
	if !errors.Is(err, ErrProvider) {
		t.Errorf("error = %v, want a classified provider failure", err)
	}
}
