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
// every workload printed, of which only the part from Start is kept. Pull runs
// before a submission is held, as an image pull does, and may block or fail
// it; Started runs once it is held, before Submit returns. It registers as
// Name, containerd when empty, each workload taking Overhead on top of what it
// declared.
type Fake struct {
	Finish   bool
	ExitCode int
	Output   string
	Start    int64
	Pull     func(ctx context.Context, id string) error
	Started  func(id string)
	Name     string
	Overhead *agentrpc.Resources

	mu        sync.Mutex
	workloads map[string]*agentrpc.Workload
	cancelled map[string]bool
}

// Info names the fake and what each of its workloads takes beyond what it
// declared.
func (f *Fake) Info(context.Context) (*agentrpc.NodeExecutor, error) {
	name := f.Name
	if name == "" {
		name = agentrpc.ExecutorContainerd
	}

	return &agentrpc.NodeExecutor{Name: name, Overhead: f.Overhead}, nil
}

// Submit holds the workload under id, once: a second submission of a held
// one starts nothing.
func (f *Fake) Submit(ctx context.Context, id string, w *agentrpc.Workload) error {
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
		f.workloads, f.cancelled = make(map[string]*agentrpc.Workload), make(map[string]bool)
	}

	f.workloads[id] = w
	f.mu.Unlock()

	if f.Started != nil {
		f.Started(id)
	}

	return nil
}

// Status reports a held workload running, succeeded when Finish is set, or
// cancelled; any other is executor.ErrUnknown.
func (f *Fake) Status(_ context.Context, id string) (*agentrpc.ExecutionStatus, error) {
	state, err := f.state(id)
	if err != nil {
		return nil, err
	}

	return &agentrpc.ExecutionStatus{State: string(state), ProviderId: id}, nil
}

// Result returns an ended workload's exit code, and fails for one still
// running.
func (f *Fake) Result(_ context.Context, id string) (*agentrpc.ExecutionResult, error) {
	state, err := f.state(id)
	if err != nil {
		return nil, err
	}

	if !state.Terminal() {
		return nil, errors.New("still running")
	}

	return agentrpc.ResultOf(&execution.Result{ExitCode: new(f.ExitCode), Duration: time.Second}), nil
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

// Logs writes a held workload's output from offset on, placed at Start first
// when offset is before it.
func (f *Fake) Logs(_ context.Context, id string, offset int64, w execution.LogWriter) error {
	if _, err := f.state(id); err != nil {
		return err
	}

	if offset < f.Start {
		if err := w.StartsAt(f.Start); err != nil {
			return err
		}

		offset = f.Start
	}

	_, err := io.WriteString(w, f.Output[min(offset, int64(len(f.Output))):])

	return err
}

// Held reports every workload with what it takes, running until it ends.
func (f *Fake) Held(context.Context) ([]*agentrpc.Held, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	held := make([]*agentrpc.Held, 0, len(f.workloads))
	for id, w := range f.workloads {
		held = append(held, &agentrpc.Held{
			ExecutionId: id,
			Resources:   agentrpc.Charge(w.GetResources(), f.Overhead),
			Running:     !f.Finish && !f.cancelled[id],
		})
	}

	return held, nil
}

// Workload returns what the workload id was submitted as, or nil when it is
// not held.
func (f *Fake) Workload(id string) *agentrpc.Workload {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.workloads[id]
}

// state is where a held workload stands: running, succeeded when Finish is
// set, or cancelled. Any other is executor.ErrUnknown.
func (f *Fake) state(id string) (execution.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.workloads[id]; !ok {
		return "", executor.ErrUnknown
	}

	switch {
	case f.cancelled[id]:
		return execution.StateCancelled, nil
	case f.Finish:
		return execution.StateSucceeded, nil
	default:
		return execution.StateRunning, nil
	}
}
