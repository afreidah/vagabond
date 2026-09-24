// -------------------------------------------------------------------------------
// Fake Providers
//
// Author: Alex Freidah
//
// In-memory providers covering the three execution families. They exist first
// to prove the interface can be satisfied without contorting an implementation,
// and second so that admission, scheduling, and dispatch can be developed and
// tested end to end before any cloud account exists.
//
// That second point is the one that matters. If the test suite needed an IBM
// account, the abstraction would already have failed.
// -------------------------------------------------------------------------------

package plugin

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ptr"
)

// -------------------------------------------------------------------------
// ASYNCHRONOUS FAKE
// -------------------------------------------------------------------------

// FakeContainerProvider is a container provider whose work outlives Submit,
// like IBM Code Engine or Google Cloud Run Jobs.
//
// SubmitErr and Capabilities are exported so a test can make the provider fail
// or advertise something unusual without a constructor per scenario.
type FakeContainerProvider struct {
	ProviderName string
	Caps         Capabilities
	SubmitErr    error
	ExitCode     int

	mu    sync.Mutex
	runs  map[execution.ID]execution.Status
	clock func() time.Time
}

// NewFakeContainerProvider returns a container provider with the standard
// container fixture's capabilities.
func NewFakeContainerProvider(name string) *FakeContainerProvider {
	return &FakeContainerProvider{
		ProviderName: name,
		Caps:         FixtureContainer(time.Now()),
		runs:         make(map[execution.ID]execution.Status),
		clock:        time.Now,
	}
}

// Name returns the routing identifier.
func (p *FakeContainerProvider) Name() string {
	return p.ProviderName
}

// Capabilities returns what this provider advertises.
func (p *FakeContainerProvider) Capabilities(context.Context) (Capabilities, error) {
	return p.Caps.Clone(), nil
}

// Submit records the run and acknowledges it without finishing the work.
func (p *FakeContainerProvider) Submit(
	_ context.Context, id execution.ID, _ *job.Task,
) (Submission, error) {
	if p.SubmitErr != nil {
		return Submission{}, p.SubmitErr
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.runs[id] = execution.Status{
		ID:         id,
		State:      execution.StateAccepted,
		ProviderID: "fake-" + id.String(),
		UpdatedAt:  p.clock(),
	}

	return Submission{
		ProviderID: p.runs[id].ProviderID,
		State:      execution.StateAccepted,
	}, nil
}

// Status reports a recorded run, which is what makes this the asynchronous
// family.
func (p *FakeContainerProvider) Status(
	_ context.Context, id execution.ID,
) (execution.Status, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	status, ok := p.runs[id]
	if !ok {
		return execution.Status{}, Internal(fmt.Errorf("%w %s", ErrUnknownExecution, id))
	}

	return status, nil
}

// Cancel marks a recorded run cancelled. Cancelling an execution that already
// finished is not an error: the caller wanted it not running, and it is not.
func (p *FakeContainerProvider) Cancel(_ context.Context, id execution.ID) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	status, ok := p.runs[id]
	if !ok {
		return Internal(fmt.Errorf("unknown execution %s", id))
	}

	if status.Terminal() {
		return nil
	}

	if err := status.To(execution.StateCancelled, p.clock()); err != nil {
		return Internal(err)
	}

	p.runs[id] = status

	return nil
}

// Result reports what a recorded run produced.
//
// Derived rather than stored: this fake exists to satisfy the interface and to
// let the registry and job plan run with no cloud account, so ExitCode is the
// one knob a test needs and anything more would be modelling a platform rather
// than the contract.
func (p *FakeContainerProvider) Result(
	_ context.Context, id execution.ID,
) (*execution.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.runs[id]; !ok {
		return nil, Internal(fmt.Errorf("unknown execution %s", id))
	}

	return &execution.Result{
		ID:       id,
		ExitCode: ptr.Of(p.ExitCode),
		Duration: time.Second,
		Logs:     []byte("fake execution output\n"),
	}, nil
}

// Advance moves a recorded run into the given state, standing in for whatever
// the platform would have done between polls.
func (p *FakeContainerProvider) Advance(id execution.ID, next execution.State) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	status, ok := p.runs[id]
	if !ok {
		return fmt.Errorf("unknown execution %s", id)
	}

	if err := status.To(next, p.clock()); err != nil {
		return err
	}

	p.runs[id] = status

	return nil
}

// -------------------------------------------------------------------------
// SYNCHRONOUS FAKE
// -------------------------------------------------------------------------

// FakeSyncProvider finishes its work inside Submit, like AWS Lambda or a
// Cloudflare worker.
//
// It embeds the not-supported helpers, which is the whole demonstration: a
// provider with no work outliving Submit declares that in two lines rather than
// writing two method bodies.
type FakeSyncProvider struct {
	StatusNotSupported
	ResultNotSupported
	CancelNotSupported

	ProviderName string
	Caps         Capabilities
	ExitCode     *int
	SubmitErr    error
}

// NewFakeFunctionProvider returns a synchronous provider with the function
// fixture's capabilities, including its fifteen minute limit.
func NewFakeFunctionProvider(name string) *FakeSyncProvider {
	return &FakeSyncProvider{
		ProviderName: name,
		Caps:         FixtureFunction(time.Now()),
		ExitCode:     ptr.Of(0),
	}
}

// NewFakeWorkerProvider returns a synchronous provider with the worker
// fixture's capabilities and no exit code, because a Wasm sandbox has no
// process to exit.
func NewFakeWorkerProvider(name string) *FakeSyncProvider {
	return &FakeSyncProvider{
		ProviderName: name,
		Caps:         FixtureWorker(time.Now()),
		ExitCode:     nil,
	}
}

// Name returns the routing identifier.
func (p *FakeSyncProvider) Name() string {
	return p.ProviderName
}

// Capabilities returns what this provider advertises.
func (p *FakeSyncProvider) Capabilities(context.Context) (Capabilities, error) {
	return p.Caps.Clone(), nil
}

// Submit runs the work and returns its result, so the caller never polls.
func (p *FakeSyncProvider) Submit(
	_ context.Context, id execution.ID, _ *job.Task,
) (Submission, error) {
	if p.SubmitErr != nil {
		return Submission{}, p.SubmitErr
	}

	result := &execution.Result{
		ID:       id,
		ExitCode: p.ExitCode,
		Duration: time.Second,
	}

	state := execution.StateSucceeded
	if !result.Succeeded() {
		state = execution.StateFailed
	}

	return Submission{
		ProviderID: "fake-" + id.String(),
		State:      state,
		Result:     result,
	}, nil
}
