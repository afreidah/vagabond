// -------------------------------------------------------------------------------
// Streaming and Progress Tests
//
// Author: Alex Freidah
//
// Streaming is optional, so half of what matters here is that a provider
// without it behaves exactly as before.
// -------------------------------------------------------------------------------

package dispatch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// streamingProvider is a scripted provider that also implements LogStreamer.
type streamingProvider struct {
	scriptedProvider

	output    string
	streamErr error

	streams   int
	cancelled bool
}

func (p *streamingProvider) StreamLogs(
	ctx context.Context, _ execution.ID, w io.Writer,
) error {
	p.streams++

	if p.streamErr != nil {
		return p.streamErr
	}

	if _, err := io.WriteString(w, p.output); err != nil {
		return err
	}

	// Hold the stream open the way a real one does, so a test can prove the
	// dispatcher closes it.
	<-ctx.Done()
	p.cancelled = true

	return ctx.Err()
}

func newStreamer(name, output string) *streamingProvider {
	return &streamingProvider{
		scriptedProvider: scriptedProvider{
			name: name, pollsToFinish: 2, finalState: execution.StateSucceeded,
		},
		output: output,
	}
}

// -------------------------------------------------------------------------
// STREAMING
// -------------------------------------------------------------------------

func TestStreamsLogsWhenSupported(t *testing.T) {
	t.Parallel()

	p := newStreamer("a", "compiling...\ndone\n")

	var logs bytes.Buffer

	d := New(newRegistry(&p.scriptedProvider),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
		WithLogs(&logs))

	// The registry must hand back the streaming type, not the embedded one.
	d.registry.(*fakeRegistry).providers["a"] = p

	if _, err := d.RunTask(t.Context(), containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if got := logs.String(); got != "compiling...\ndone\n" {
		t.Errorf("logs = %q", got)
	}

	if p.streams != 1 {
		t.Errorf("opened %d streams, want 1", p.streams)
	}
}

// The stream is closed when the execution ends, or it outlives the run and the
// tail of a build lands under the result.
func TestStreamIsClosedWhenTheExecutionEnds(t *testing.T) {
	t.Parallel()

	p := newStreamer("a", "output\n")

	var logs bytes.Buffer

	d := New(newRegistry(&p.scriptedProvider),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
		WithLogs(&logs))
	d.registry.(*fakeRegistry).providers["a"] = p

	if _, err := d.RunTask(t.Context(), containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if !p.cancelled {
		t.Error("the stream was left open after the execution finished")
	}
}

// A provider with no StreamLogs method runs exactly as before.
func TestNoStreamingWhenUnsupported(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded,
	}

	var logs bytes.Buffer

	d := New(newRegistry(p),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
		WithLogs(&logs))

	if _, err := d.RunTask(t.Context(), containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if logs.Len() != 0 {
		t.Errorf("logs = %q, want nothing", logs.String())
	}
}

// Nobody asked for output, so nothing is streamed even where it is available.
func TestNoStreamingWithoutAWriter(t *testing.T) {
	t.Parallel()

	p := newStreamer("a", "output\n")

	d := New(newRegistry(&p.scriptedProvider),
		WithSleeper(func(context.Context, time.Duration) error { return nil }))
	d.registry.(*fakeRegistry).providers["a"] = p

	if _, err := d.RunTask(t.Context(), containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if p.streams != 0 {
		t.Error("streamed with no writer configured")
	}
}

// Losing the view is not a reason to abandon work the provider is still doing.
func TestStreamFailureDoesNotFailTheExecution(t *testing.T) {
	t.Parallel()

	p := newStreamer("a", "")
	p.streamErr = errors.New("stream broke")

	var logs bytes.Buffer

	d := New(newRegistry(&p.scriptedProvider),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
		WithLogs(&logs))
	d.registry.(*fakeRegistry).providers["a"] = p

	outcome, err := d.RunTask(t.Context(), containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("a broken stream failed the execution: %v", err)
	}

	if !outcome.Succeeded() {
		t.Error("the result was lost")
	}
}

// -------------------------------------------------------------------------
// PROGRESS
// -------------------------------------------------------------------------

func TestProgressReportsEachStateChange(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 3, finalState: execution.StateSucceeded,
	}

	var states []execution.State

	d := New(newRegistry(p),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
		WithProgress(func(e Event) { states = append(states, e.State) }))

	if _, err := d.RunTask(t.Context(), containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	// Accepted from the submission and running from the first poll. The
	// repeated running polls report nothing, and neither does the terminal
	// state: the caller announces that once the output has arrived.
	want := []execution.State{
		execution.StateAccepted,
		execution.StateRunning,
	}

	if len(states) != len(want) {
		t.Fatalf("states = %v, want %v", states, want)
	}

	for i := range want {
		if states[i] != want[i] {
			t.Errorf("states = %v, want %v", states, want)

			break
		}
	}
}

func TestProgressCarriesTheAttemptNumber(t *testing.T) {
	t.Parallel()

	first := &scriptedProvider{
		name: "a", submitErr: plugin.Infrastructure(errors.New("503")),
	}
	second := &scriptedProvider{
		name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded,
	}

	var events []Event

	d := New(newRegistry(first, second),
		WithSleeper(func(context.Context, time.Duration) error { return nil }),
		WithProgress(func(e Event) { events = append(events, e) }))

	if _, err := d.RunTask(t.Context(), containerTask(t, reroutingRetry(2)), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	// The first attempt never reports: it failed inside Submit, before there
	// was a state to announce.
	if len(events) == 0 {
		t.Fatal("no events were reported")
	}

	if events[0].Attempt != 2 || events[0].Provider != "b" {
		t.Errorf("first event = %+v, want attempt 2 on b", events[0])
	}
}

// -------------------------------------------------------------------------
// RELEASE
// -------------------------------------------------------------------------

// releasingProvider leaves a resource behind, as Cloud Run does.
type releasingProvider struct {
	scriptedProvider

	releases      int
	releasedAfter int // how many Result calls had happened by then
}

func (p *releasingProvider) Release(context.Context, execution.ID) error {
	p.releases++
	p.releasedAfter = p.results

	return nil
}

// Every run leaves a Cloud Run job against a per-region quota, so the normal
// path has to delete it rather than leaving the sweep as the only cleanup.
func TestReleaseIsCalledOnSuccess(t *testing.T) {
	t.Parallel()

	p := &releasingProvider{
		scriptedProvider: scriptedProvider{
			name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded,
		},
	}

	d := newDispatcher(t, newRegistry(&p.scriptedProvider))
	d.registry.(*fakeRegistry).providers["a"] = p

	if _, err := d.RunTask(t.Context(), containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if p.releases != 1 {
		t.Errorf("released %d times, want 1", p.releases)
	}

	// Releasing may destroy what Result reads, so the order is load-bearing.
	if p.releasedAfter != 1 {
		t.Error("released before the result was fetched")
	}
}

// A failing build still leaves the resource behind.
func TestReleaseIsCalledOnAFailedWorkload(t *testing.T) {
	t.Parallel()

	p := &releasingProvider{
		scriptedProvider: scriptedProvider{
			name: "a", pollsToFinish: 1, finalState: execution.StateFailed, exitCode: 1,
		},
	}

	d := newDispatcher(t, newRegistry(&p.scriptedProvider))
	d.registry.(*fakeRegistry).providers["a"] = p

	if _, err := d.RunTask(t.Context(), containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if p.releases != 1 {
		t.Errorf("released %d times, want 1", p.releases)
	}
}

// A provider with nothing to release is not asked to.
func TestNoReleaseWhenUnsupported(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded,
	}

	if _, err := newDispatcher(t, newRegistry(p)).
		RunTask(t.Context(), containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}
}

// -------------------------------------------------------------------------
// ABANDONMENT
// -------------------------------------------------------------------------

// A caller that gave up leaves work running and billing, so it is stopped.
func TestCancelledRunStopsTheExecution(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 100, finalState: execution.StateSucceeded,
	}

	ctx, cancel := context.WithCancel(t.Context())

	polls := 0

	d := New(newRegistry(p), WithSleeper(func(context.Context, time.Duration) error {
		polls++
		if polls > 1 {
			cancel()

			return context.Canceled
		}

		return nil
	}))

	_, err := d.RunTask(ctx, containerTask(t, nil), nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}

	if p.cancels != 1 {
		t.Errorf("called Cancel %d times, want 1", p.cancels)
	}
}

// A provider that failed on its own is not still running, so nothing is
// cancelled.
func TestProviderFailureDoesNotCancel(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", statusErr: plugin.Infrastructure(errors.New("503")),
	}

	if _, err := newDispatcher(t, newRegistry(p)).
		RunTask(t.Context(), containerTask(t, nil), nil, nil); err == nil {
		t.Fatal("expected a failure")
	}

	if p.cancels != 0 {
		t.Errorf("called Cancel %d times on a provider failure, want 0", p.cancels)
	}
}
