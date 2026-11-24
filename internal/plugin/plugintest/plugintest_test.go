// -------------------------------------------------------------------------------
// Provider Test Kit Tests
//
// Author: Alex Freidah
//
// The live helpers against a provider that runs nothing: it reports the states
// it is given in turn, exits 3, and prints what a round trip expects, so the
// waits, the capture and the checks run without a cloud account.
// -------------------------------------------------------------------------------

package plugintest

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// quick waits barely at all, since nothing here takes time.
var quick = Live{Poll: time.Millisecond, Deadline: time.Second}

// scripted is a provider whose every execution moves through states, one per
// Status call and then the last for good, exits 3, and prints both streams. A
// running state holds until the output is printed, as a task's sleep does.
type scripted struct {
	plugin.Provider

	mu        sync.Mutex
	states    []execution.State
	printed   bool
	cancelled bool
}

// output is what every scripted run prints, the round trip's script with its
// $ passed through.
const output = "hello-stdout-$BUILD_ID\nhello-stderr\n"

// Submit accepts any task.
func (s *scripted) Submit(context.Context, execution.ID, *job.Task) (plugin.Submission, error) {
	return plugin.Submission{ProviderID: "scripted", State: execution.StateAccepted}, nil
}

// Status reports the next state, keeping the last.
func (s *scripted) Status(_ context.Context, id execution.ID) (execution.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.states[0]
	if len(s.states) > 1 && (state != execution.StateRunning || s.printed) {
		s.states = s.states[1:]
	}

	return execution.Status{ID: id, State: state}, nil
}

// Result reports exit 3.
func (s *scripted) Result(_ context.Context, id execution.ID) (*execution.Result, error) {
	return &execution.Result{ID: id, ExitCode: new(3)}, nil
}

// Cancel records that the run was cleaned up.
func (s *scripted) Cancel(context.Context, execution.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cancelled = true

	return nil
}

// CaptureLogs writes what of the run's output log lacks, then waits for the
// run to be over. Its first call fails, as a cut stream does, so the capture
// has to make it again.
func (s *scripted) CaptureLogs(ctx context.Context, _ execution.ID, log execution.Log, ended <-chan struct{}) error {
	if _, err := io.WriteString(plugin.SkipWriter(log, log.Len()), output); err != nil {
		return err
	}

	s.mu.Lock()
	first := !s.printed
	s.printed = true
	s.mu.Unlock()

	if first {
		return io.ErrUnexpectedEOF
	}

	select {
	case <-ended:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// -------------------------------------------------------------------------
// LIVE RUNS
// -------------------------------------------------------------------------

// A round trip passes on a run that fails with exit 3 and prints both
// streams, captured while it runs, and cancels the run when the test ends.
func TestLive_RoundTrip(t *testing.T) {
	p := &scripted{states: []execution.State{execution.StateRunning, execution.StateFailed}}

	t.Run("round trip", func(t *testing.T) {
		if id := quick.RoundTrip(t, p, job.Resources{}); id.IsZero() {
			t.Error("RoundTrip() returned no execution id")
		}
	})

	if !p.cancelled {
		t.Error("the run was not cancelled when its test ended")
	}
}

// ReadAll captures a finished run's output once, however often the capture
// is made.
func TestLive_ReadAll(t *testing.T) {
	p := &scripted{states: []execution.State{execution.StateFailed}}

	if got := quick.ReadAll(t, p, testkit.NewID(t)); got != output {
		t.Errorf("ReadAll() = %q, want %q", got, output)
	}
}

// AwaitRunning returns once the run is seen running.
func TestLive_AwaitRunning(t *testing.T) {
	p := &scripted{states: []execution.State{execution.StateAccepted, execution.StateRunning}}

	quick.AwaitRunning(t, p, testkit.NewID(t))
}

// -------------------------------------------------------------------------
// CONFIGURATION
// -------------------------------------------------------------------------

// The documented example task reads as a container with its image.
func TestContainerTask(t *testing.T) {
	task := ContainerTask(t)

	if image, ok := task.ConfigString(job.ConfigImage); !ok || image != "golang:1.27" || task.Driver != job.DriverContainer {
		t.Errorf("ContainerTask() = %+v, image %q; want a golang:1.27 container", task, image)
	}

	if Body(t, `region = "r"`) == nil {
		t.Error("Body() = nil")
	}
}
