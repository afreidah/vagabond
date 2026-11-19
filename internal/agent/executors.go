// -------------------------------------------------------------------------------
// Executors
//
// Author: Alex Freidah
//
// The node's executors behind one: a workload goes to the executor it names,
// and a call about an execution to whichever executor holds it, asked in turn,
// so nothing has to remember where each execution went.
// -------------------------------------------------------------------------------

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/afreidah/vagabond/internal/agent/executor"
	"github.com/afreidah/vagabond/internal/agentrpc"
)

// errNoExecutor reports a workload naming an executor the node does not run.
var errNoExecutor = errors.New("the node has no such executor")

// Executor runs one kind of workload on the node: containers on containerd, or
// microVMs through vagabond-firecracker. Anything it does not hold is
// executor.ErrUnknown.
type Executor interface {
	Info(ctx context.Context) (*agentrpc.NodeExecutor, error)
	Submit(ctx context.Context, id string, w *agentrpc.Workload) error
	Status(ctx context.Context, id string) (*agentrpc.ExecutionStatus, error)
	Result(ctx context.Context, id string) (*agentrpc.ExecutionResult, error)
	Cancel(ctx context.Context, id string) error
	Release(ctx context.Context, id string) error
	Logs(ctx context.Context, id string, offset int64, w io.Writer) error
	Held(ctx context.Context) ([]*agentrpc.Held, error)
}

// executors are the node's executors in the order given, info[i] what all[i]
// registers as.
type executors struct {
	all  []Executor
	info []*agentrpc.NodeExecutor
}

// newExecutors asks each executor what it is, refusing one that does not
// answer or takes a name another already has.
func newExecutors(ctx context.Context, execs ...Executor) (*executors, error) {
	out := &executors{}

	// Each is asked once, at startup, and registered as it answers.
	for _, exec := range execs {
		info, err := exec.Info(ctx)
		if err != nil {
			return nil, err
		}

		// A workload names its executor, so two by one name could not be told apart.
		if _, _, err := out.named(info.GetName()); err == nil {
			return nil, fmt.Errorf("two executors are named %q", info.GetName())
		}

		out.all, out.info = append(out.all, exec), append(out.info, info)
	}

	return out, nil
}

// named is the executor called name and what it registers as, or
// errNoExecutor when the node runs none by that name.
func (e *executors) named(name string) (Executor, *agentrpc.NodeExecutor, error) {
	for i, info := range e.info {
		if info.GetName() == name {
			return e.all[i], info, nil
		}
	}

	return nil, nil, fmt.Errorf("%w: %q", errNoExecutor, name)
}

// holding makes call on each executor in turn until one holds the execution,
// and returns its answer: executor.ErrUnknown only when none of them does.
func (e *executors) holding(call func(Executor) error) error {
	err := executor.ErrUnknown

	// An execution is on one executor at most; any other answers it unknown.
	for _, exec := range e.all {
		if err = call(exec); !errors.Is(err, executor.ErrUnknown) {
			return err
		}
	}

	return err
}

// Release deletes an execution from every executor, each taking one it does
// not hold as released already, and one failing not stopping the others.
func (e *executors) Release(ctx context.Context, id string) error {
	errs := make([]error, 0, len(e.all))
	for _, exec := range e.all {
		errs = append(errs, exec.Release(ctx, id))
	}

	return errors.Join(errs...)
}

// Held is every workload the node's executors hold, in the order the executors
// were given.
func (e *executors) Held(ctx context.Context) ([]*agentrpc.Held, error) {
	var held []*agentrpc.Held

	// One executor that cannot say what it holds leaves the node's room unknown.
	for _, exec := range e.all {
		h, err := exec.Held(ctx)
		if err != nil {
			return nil, err
		}

		held = append(held, h...)
	}

	return held, nil
}
