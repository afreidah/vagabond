// -------------------------------------------------------------------------------
// Fake Executor
//
// Author: Alex Freidah
//
// An executor that runs nothing, for tests of the agent and of what talks to
// it: workloads are held in memory, run until released or end at once, and
// print a fixed output. Hooks around a submission let a test pause or fail one
// mid-pull.
// -------------------------------------------------------------------------------

package executortest

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
)

// Fake holds workloads in memory. Each runs until released, or ends at once
// with ExitCode when Finish is set; Cancel ends one cancelled. Output is what
// every workload printed. Pull runs before a submission is held, as an image
// pull does, and may block or fail it; Started runs once it is held, before
// Submit returns.
type Fake struct {
	Finish   bool
	ExitCode int
	Output   string
	Pull     func(ctx context.Context, id string) error
	Started  func(id string)

	mu        sync.Mutex
	workloads map[string]*executor.Spec
	cancelled map[string]bool
}

// Submit holds the workload under id, once: a second submission of a held
// one starts nothing.
func (f *Fake) Submit(ctx context.Context, id string, spec *executor.Spec) error {
	if f.Workload(id) != nil {
		return nil
	}

	if f.Pull != nil {
		if err := f.Pull(ctx, id); err != nil {
			return err
		}
	}

	f.mu.Lock()
	if f.workloads == nil {
		f.workloads, f.cancelled = make(map[string]*executor.Spec), make(map[string]bool)
	}

	f.workloads[id] = spec
	f.mu.Unlock()

	if f.Started != nil {
		f.Started(id)
	}

	return nil
}

// Status reports a held workload running, succeeded when Finish is set, or
// cancelled; any other is executor.ErrUnknown.
func (f *Fake) Status(_ context.Context, id string) (execution.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.workloads[id]; !ok {
		return execution.Status{}, executor.ErrUnknown
	}

	state := execution.StateRunning

	switch {
	case f.cancelled[id]:
		state = execution.StateCancelled
	case f.Finish:
		state = execution.StateSucceeded
	}

	return execution.Status{State: state, ProviderID: id}, nil
}

// Result returns an ended workload's exit code, and fails for one still
// running.
func (f *Fake) Result(ctx context.Context, id string) (*execution.Result, error) {
	st, err := f.Status(ctx, id)
	if err != nil {
		return nil, err
	}

	if !st.State.Terminal() {
		return nil, errors.New("still running")
	}

	return &execution.Result{ExitCode: new(f.ExitCode), Duration: time.Second}, nil
}

// Cancel ends the workload cancelled.
func (f *Fake) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.workloads[id]; ok {
		f.cancelled[id] = true
	}

	return nil
}

// Release forgets the workload.
func (f *Fake) Release(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.workloads, id)
	delete(f.cancelled, id)

	return nil
}

// Logs writes a held workload's output from offset on.
func (f *Fake) Logs(ctx context.Context, id string, offset int64, w io.Writer) error {
	if _, err := f.Status(ctx, id); err != nil {
		return err
	}

	_, err := io.WriteString(w, f.Output[min(offset, int64(len(f.Output))):])

	return err
}

// Held reports every workload with what it declared, running until it ends.
func (f *Fake) Held(context.Context) ([]*agentrpc.Held, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	held := make([]*agentrpc.Held, 0, len(f.workloads))
	for id, spec := range f.workloads {
		held = append(held, &agentrpc.Held{
			ExecutionId: id,
			Resources:   &agentrpc.Resources{Cpu: spec.CPU, Memory: spec.Memory},
			Running:     !f.Finish && !f.cancelled[id],
		})
	}

	return held, nil
}

// Runtimes offers only the default runtime.
func (f *Fake) Runtimes() []string {
	return []string{executor.DefaultRuntime}
}

// Workload returns what the workload id was submitted as, or nil when it is
// not held.
func (f *Fake) Workload(id string) *executor.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.workloads[id]
}
