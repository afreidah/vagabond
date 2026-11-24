// -------------------------------------------------------------------------------
// Fake Providers
//
// Author: Alex Freidah
//
// In-memory providers covering the three execution families. They prove the
// interface can be satisfied without contorting an implementation, and let
// admission, scheduling, and dispatch be tested end to end with no cloud
// account. An in-memory log takes a provider's capture under test.
// -------------------------------------------------------------------------------

package plugin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// fakeOutput is what every fake container run prints.
const fakeOutput = "fake execution output\n"

// -------------------------------------------------------------------------
// ASYNCHRONOUS FAKE
// -------------------------------------------------------------------------

// FakeContainerProvider is a container provider whose work outlives Submit,
// like IBM Code Engine or Google Cloud Run Jobs.
//
// The exported fields let a test make the provider fail or advertise something
// unusual without a constructor per scenario.
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

// Result reports what a recorded run produced, derived rather than stored:
// ExitCode is the one knob a test needs. The output is read by CaptureLogs.
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
		ExitCode: new(p.ExitCode),
		Duration: time.Second,
	}, nil
}

// CaptureLogs writes what of a recorded run's output log does not hold yet,
// then waits for the run to be over or ctx to end.
func (p *FakeContainerProvider) CaptureLogs(
	ctx context.Context, id execution.ID, log execution.Log, ended <-chan struct{},
) error {
	if _, err := p.Status(ctx, id); err != nil {
		return err
	}

	if _, err := io.WriteString(SkipWriter(log, log.Len()), fakeOutput); err != nil {
		return err
	}

	select {
	case <-ended:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
// Cloudflare worker, and embeds the not-supported helpers instead of writing
// those method bodies.
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
		ExitCode:     new(0),
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

	// A non-zero exit fails; no exit code, as a worker has none, succeeds.
	state := execution.StateSucceeded
	if p.ExitCode != nil && *p.ExitCode != 0 {
		state = execution.StateFailed
	}

	return Submission{
		ProviderID: "fake-" + id.String(),
		State:      state,
		Result:     result,
	}, nil
}

// -------------------------------------------------------------------------
// LOG
// -------------------------------------------------------------------------

// MemoryLog is a Log held in memory for a capture under test, safe to read
// while the capture writes it. Its zero value is empty.
type MemoryLog struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	start int64
}

// Write appends p.
func (l *MemoryLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(p)
}

// StartsAt drops what the log holds, so that what is written next begins at
// offset.
func (l *MemoryLog) StartsAt(offset int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.buf.Reset()
	l.start = offset

	return nil
}

// Len is where what is written next begins.
func (l *MemoryLog) Len() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.start + int64(l.buf.Len())
}

// WriteTo writes what the log holds to w.
func (l *MemoryLog) WriteTo(w io.Writer) (int64, error) {
	n, err := io.WriteString(w, l.String())

	return int64(n), err
}

// String is what the log holds.
func (l *MemoryLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.String()
}

// Start is where what the log holds begins.
func (l *MemoryLog) Start() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.start
}
