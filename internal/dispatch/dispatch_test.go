// -------------------------------------------------------------------------------
// Dispatch Tests
//
// Author: Alex Freidah
//
// Most scenarios here are a provider failing to answer. They run on a
// scripted provider rather than the shared fake in internal/plugin, which
// advances only when a test tells it to.
// -------------------------------------------------------------------------------

package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ledger"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
	"github.com/afreidah/vagabond/internal/state/memory"
)

// -------------------------------------------------------------------------
// DOUBLES
// -------------------------------------------------------------------------

// scriptedProvider answers however a test needs it to: each *Err field fails
// the call it names, and the counters record the calls made.
type scriptedProvider struct {
	name string

	submitErr error
	statusErr error
	resultErr error

	pollsToFinish int // Status calls before a terminal state
	finalState    execution.State
	exitCode      int
	noExitCode    bool             // no exit code, as a worker has none
	synchronous   bool             // finishes inside Submit, like a function
	billed        *quota.Execution // what the platform charged; usually nil
	logsTruncated bool             // the result's output is not all of it
	startedAt     time.Time        // when Status says the run started; usually zero

	submits int
	polls   int
	results int
	cancels int
}

// Name returns the provider's configured name.
func (p *scriptedProvider) Name() string { return p.name }

// exit is the result's exit code: exitCode, or none with noExitCode.
func (p *scriptedProvider) exit() *int {
	if p.noExitCode {
		return nil
	}

	return new(p.exitCode)
}

// Capabilities reports a fresh container fixture.
func (p *scriptedProvider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.FixtureContainer(time.Now()), nil
}

// Submit accepts the task, or finishes it at once when synchronous.
func (p *scriptedProvider) Submit(
	_ context.Context, id execution.ID, _ *job.Task,
) (plugin.Submission, error) {
	p.submits++

	if p.submitErr != nil {
		return plugin.Submission{}, p.submitErr
	}

	if p.synchronous {
		return plugin.Submission{
			ProviderID: p.name + "-" + id.String(),
			State:      execution.StateSucceeded,
			Result:     &execution.Result{ID: id, ExitCode: p.exit()},
		}, nil
	}

	return plugin.Submission{
		ProviderID: p.name + "-" + id.String(),
		State:      execution.StateAccepted,
	}, nil
}

// Status reports running until pollsToFinish calls, then finalState.
func (p *scriptedProvider) Status(
	_ context.Context, id execution.ID,
) (execution.Status, error) {
	p.polls++

	if p.statusErr != nil {
		return execution.Status{}, p.statusErr
	}

	state := execution.StateRunning
	if p.polls >= p.pollsToFinish {
		state = p.finalState
	}

	return execution.Status{ID: id, State: state, StartedAt: p.startedAt}, nil
}

// Result returns a one-second run with the scripted exit code and billing.
func (p *scriptedProvider) Result(
	_ context.Context, id execution.ID,
) (*execution.Result, error) {
	p.results++

	if p.resultErr != nil {
		return nil, p.resultErr
	}

	return &execution.Result{
		ID:            id,
		ExitCode:      p.exit(),
		Duration:      time.Second,
		Billed:        p.billed,
		Logs:          []byte("scripted output\n"),
		LogsTruncated: p.logsTruncated,
	}, nil
}

// Cancel counts the call and succeeds.
func (p *scriptedProvider) Cancel(context.Context, execution.ID) error {
	p.cancels++

	return nil
}

// fakeRegistry holds whatever providers a test declared, and the ledger they
// are charged against.
type fakeRegistry struct {
	inputs         []scheduler.Input
	providers      map[string]plugin.Provider
	ledger         *ledger.Ledger
	executions     *memory.Executions
	recredentialed int
}

// Inputs returns the declared inputs with each provider's usage filled in.
func (r *fakeRegistry) Inputs(
	namespace string, usage func(namespace, provider string) (total, share quota.PoolUsage),
) []scheduler.Input {
	inputs := make([]scheduler.Input, len(r.inputs))
	copy(inputs, r.inputs)

	if usage != nil {
		for i := range inputs {
			inputs[i].Usage, inputs[i].ShareUsage = usage(namespace, inputs[i].Provider)
		}
	}

	return inputs
}

// Provider returns the named provider, if the test declared it.
func (r *fakeRegistry) Provider(name string) (plugin.Provider, bool) {
	p, ok := r.providers[name]

	return p, ok
}

// Recredential hands a provider that takes one a fresh credential, as the
// registry does after resolving its source again, and counts the refreshes.
func (r *fakeRegistry) Recredential(ctx context.Context, name string) (bool, error) {
	recredentialer, ok := r.providers[name].(plugin.Recredentialer)
	if !ok {
		return false, nil
	}

	r.recredentialed++

	return true, recredentialer.Recredential(ctx, []byte("fresh"))
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// ns is the namespace every dispatch here runs in. It declares no shares.
const ns = "default"

// origin is what every task here runs for.
var origin = Origin{Namespace: ns, Job: "ci"}

// begin records a one-task dispatch for origin's job under d's lease and
// returns the origin to run it under.
func begin(t *testing.T, d *Dispatcher) Origin {
	t.Helper()

	o, err := d.Begin(t.Context(), origin, &job.Job{Name: origin.Job, Tasks: []job.Task{{Name: "test"}}})
	if err != nil {
		t.Fatalf("Begin() = %v", err)
	}

	return o
}

// runTaskIn runs task in a dispatch of its own, begun and finished as the
// server does, so every runner has finished its steps when it returns.
func (d *Dispatcher) runTaskIn(
	t *testing.T, task *job.Task, routing *job.Routing, eval *hcl.EvalContext,
) (*TaskOutcome, error) {
	t.Helper()

	o := begin(t, d)
	outcome, err := d.RunTask(t.Context(), o, task, routing, eval)

	job := &JobOutcome{Job: origin.Job, Dispatch: o.Dispatch}
	if outcome != nil {
		job.Tasks = append(job.Tasks, *outcome)
	}

	d.Finish(t.Context(), o, job, err)

	return outcome, err
}

// runJob begins, runs and finishes j as the CLI and server do, and returns
// what Run returned.
func runJob(t *testing.T, d *Dispatcher, o Origin, j *job.Job) (*JobOutcome, error) {
	t.Helper()

	o, err := d.Begin(t.Context(), o, j)
	if err != nil {
		t.Fatalf("Begin() = %v", err)
	}

	outcome, err := d.Run(t.Context(), o, j, nil)
	d.Finish(t.Context(), o, outcome, err)

	return outcome, err
}

// record reads back what the registry's store holds for id.
func record(t *testing.T, reg *fakeRegistry, id execution.ID) *execution.Record {
	t.Helper()

	r, err := reg.executions.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get(%s) = %v", id, err)
	}

	return r
}

// usageOf re-reads the registry's ledger and returns a provider's total usage.
func usageOf(reg *fakeRegistry, provider string) quota.PoolUsage {
	_ = reg.ledger.Refresh(context.Background())
	usage, _ := reg.ledger.PoolUsage(ns, provider)

	return usage
}

// newRegistry registers providers, each healthy with container budgets and
// charged a little more than the one before, so ranking follows declaration
// order.
func newRegistry(providers ...*scriptedProvider) *fakeRegistry {
	reg := &fakeRegistry{providers: map[string]plugin.Provider{}, executions: memory.NewExecutions()}

	limits := make(map[string]quota.Limits, len(providers))
	baseline := make(ledger.Usage, len(providers))
	period := quota.PeriodMonthly.Key(time.Now())

	for i, p := range providers {
		reg.providers[p.name] = p
		limits[p.name] = quota.FixtureContainer()
		baseline[ledger.Key{Provider: p.name, Pool: "requests", Period: period}] = int64(10_000 * (i + 1))

		reg.inputs = append(reg.inputs, scheduler.Input{
			Provider:     p.name,
			Capabilities: plugin.FixtureContainer(time.Now()),
			Limits:       limits[p.name],
			Enabled:      true,
			Healthy:      true,
		})
	}

	// A memory store cannot fail to read.
	reg.ledger, _ = ledger.New(context.Background(), quota.Budgets{Totals: limits}, ledger.NewMemory(baseline))

	return reg
}

// over builds a dispatcher charging the registry's own ledger.
func over(reg *fakeRegistry, opts ...Option) *Dispatcher {
	return New(reg, reg.ledger, reg.executions, opts...)
}

// newDispatcher builds one that never actually waits.
func newDispatcher(t *testing.T, reg *fakeRegistry) *Dispatcher {
	t.Helper()

	return over(reg, WithSleeper(func(context.Context, time.Duration) error {
		return nil
	}))
}

// rawBlock parses src as the body of a task's config block.
func rawBlock(t *testing.T, src string) *job.RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing failed: %s", diags.Error())
	}

	return &job.RawBlock{Body: f.Body}
}

// containerTask is a task any container provider can run.
func containerTask(t *testing.T, retry *job.Retry) *job.Task {
	t.Helper()

	return &job.Task{
		Name:      "test",
		Driver:    job.DriverContainer,
		Config:    rawBlock(t, "image = \"alpine:3.20\"\n"),
		Resources: &job.Resources{CPU: new(1000), Memory: new(512)},
		Timeout:   new(job.Duration("5m")),
		Retry:     retry,
	}
}

// reroutingRetry allows n attempts across providers.
func reroutingRetry(n int) *job.Retry {
	return &job.Retry{Attempts: new(n), Reroute: new(true)}
}

// -------------------------------------------------------------------------
// THE HAPPY PATH
// -------------------------------------------------------------------------

// A submitted task is polled until terminal and its result fetched once.
func TestRunTaskPollsUntilTerminal(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 3, finalState: execution.StateSucceeded,
	}

	outcome, err := newDispatcher(t, newRegistry(p)).
		runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if !outcome.Succeeded() {
		t.Error("a zero exit code did not report success")
	}

	if p.polls != 3 {
		t.Errorf("polled %d times, want 3", p.polls)
	}

	if p.results != 1 {
		t.Errorf("fetched the result %d times, want once", p.results)
	}

	if outcome.Provider != "a" {
		t.Errorf("provider = %q, want a", outcome.Provider)
	}
}

// A function invocation returns its answer from Submit and has nothing to
// poll.
func TestRunTaskSkipsPollingWhenSynchronous(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", synchronous: true}

	outcome, err := newDispatcher(t, newRegistry(p)).
		runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if p.polls != 0 || p.results != 0 {
		t.Errorf("polled %d and fetched %d, want neither", p.polls, p.results)
	}

	if !outcome.Succeeded() {
		t.Error("the synchronous result was lost")
	}
}

// -------------------------------------------------------------------------
// THE RULE
// -------------------------------------------------------------------------

// A failing build is an answer. Running it again somewhere else spends
// capacity to be told the same thing.
func TestWorkloadFailureIsNotRerouted(t *testing.T) {
	t.Parallel()

	first := &scriptedProvider{
		name: "a", pollsToFinish: 1, finalState: execution.StateFailed, exitCode: 1,
	}
	second := &scriptedProvider{
		name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded,
	}

	outcome, err := newDispatcher(t, newRegistry(first, second)).
		runTaskIn(t, containerTask(t, reroutingRetry(3)), nil, nil)
	if err != nil {
		t.Fatalf("a non-zero exit code was reported as a dispatch failure: %v", err)
	}

	if outcome.Succeeded() {
		t.Error("exit code 1 reported success")
	}

	if second.submits != 0 {
		t.Error("a failing build was retried on another provider")
	}

	if len(outcome.Attempts) != 1 {
		t.Errorf("made %d attempts, want 1", len(outcome.Attempts))
	}
}

// The provider's state is the verdict. A task it reports failed with no exit
// code, as Cloud Run does for one stopped before its process ran, fails.
func TestFailedStateWithoutAnExitCodeFails(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateFailed, noExitCode: true}

	outcome, err := newDispatcher(t, newRegistry(p)).
		runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask() = %v", err)
	}

	if outcome.Succeeded() {
		t.Error("a failed state with no exit code reported success")
	}
}

// A task with no exit code at all, as a worker has none, succeeds on the
// provider's word.
func TestSucceededStateWithoutAnExitCodeSucceeds(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", synchronous: true, noExitCode: true}

	outcome, err := newDispatcher(t, newRegistry(p)).
		runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil || !outcome.Succeeded() {
		t.Errorf("RunTask() = %+v, %v; want a success", outcome, err)
	}
}

// An infrastructure failure produced no verdict, so another provider may
// still produce one.
func TestInfrastructureFailureReroutes(t *testing.T) {
	t.Parallel()

	first := &scriptedProvider{
		name: "a", submitErr: plugin.Infrastructure(errors.New("503")),
	}
	second := &scriptedProvider{
		name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded,
	}

	outcome, err := newDispatcher(t, newRegistry(first, second)).
		runTaskIn(t, containerTask(t, reroutingRetry(2)), nil, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if outcome.Provider != "b" {
		t.Errorf("answered by %q, want b", outcome.Provider)
	}

	if !outcome.Rerouted() {
		t.Error("the reroute was not recorded")
	}

	// The abandoned attempt keeps the failure that explains the reroute
	if outcome.Attempts[0].Err == nil {
		t.Error("the first attempt recorded no error")
	}
}

// Giving up during the backoff before a reroute prepares nothing for the next
// provider, so nothing is left reserved or recorded for it.
func TestCancelDuringTheBackoffHoldsNothing(t *testing.T) {
	t.Parallel()

	first := &scriptedProvider{
		name: "a", submitErr: plugin.Infrastructure(errors.New("503")), statusErr: plugin.ErrUnknownExecution,
	}
	second := &scriptedProvider{name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(first, second)

	ctx, cancel := context.WithCancel(t.Context())

	// The only wait is the backoff, since nothing here fails a write
	d := over(reg, WithSleeper(func(context.Context, time.Duration) error {
		cancel()

		return context.Canceled
	}))

	o := begin(t, d)
	_, err := d.RunTask(ctx, o, containerTask(t, reroutingRetry(2)), nil, nil)
	d.Finish(t.Context(), o, nil, err)

	records, rerr := reg.executions.DispatchExecutions(t.Context(), o.Dispatch)
	if rerr != nil || len(records) != 1 || second.submits != 0 {
		t.Errorf("%d records, %v, after %d submissions to b; want only the first attempt's", len(records), rerr, second.submits)
	}

	if got := usageOf(reg, "b")["compute"]; got != 0 {
		t.Errorf("b compute = %d, want nothing reserved", got)
	}
}

// An internal failure reproduces on every provider, so it is not rerouted.
func TestInternalFailureDoesNotReroute(t *testing.T) {
	t.Parallel()

	first := &scriptedProvider{
		name: "a", submitErr: plugin.Internal(errors.New("malformed request")),
	}
	second := &scriptedProvider{
		name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded,
	}

	_, err := newDispatcher(t, newRegistry(first, second)).
		runTaskIn(t, containerTask(t, reroutingRetry(3)), nil, nil)
	if err == nil {
		t.Fatal("an internal failure was swallowed")
	}

	if second.submits != 0 {
		t.Error("an internal failure was rerouted")
	}
}

// -------------------------------------------------------------------------
// BUDGET
// -------------------------------------------------------------------------

// attempts is total attempts, not attempts after the first.
func TestAttemptsIsTotalAttempts(t *testing.T) {
	t.Parallel()

	down := func(name string) *scriptedProvider {
		return &scriptedProvider{
			name: name, submitErr: plugin.Infrastructure(errors.New("503")),
		}
	}

	a, b, c := down("a"), down("b"), down("c")

	outcome, err := newDispatcher(t, newRegistry(a, b, c)).
		runTaskIn(t, containerTask(t, reroutingRetry(2)), nil, nil)
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("error = %v, want ErrExhausted", err)
	}

	if len(outcome.Attempts) != 2 {
		t.Errorf("made %d attempts, want exactly 2", len(outcome.Attempts))
	}

	if c.submits != 0 {
		t.Error("a third provider was tried on a budget of two")
	}
}

// A task that said nothing about retrying asked for the work to be done once.
func TestNoRetryBlockIsOneAttempt(t *testing.T) {
	t.Parallel()

	a := &scriptedProvider{name: "a", submitErr: plugin.Infrastructure(errors.New("503"))}
	b := &scriptedProvider{name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded}

	_, err := newDispatcher(t, newRegistry(a, b)).
		runTaskIn(t, containerTask(t, nil), nil, nil)
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("error = %v, want ErrExhausted", err)
	}

	if b.submits != 0 {
		t.Error("a task with no retry block was sent to a second provider")
	}
}

// Without reroute the task gets one provider, however many attempts it asked
// for: retrying in place spends capacity on a provider that just failed.
func TestAttemptsWithoutRerouteStaysPut(t *testing.T) {
	t.Parallel()

	a := &scriptedProvider{name: "a", submitErr: plugin.Infrastructure(errors.New("503"))}
	b := &scriptedProvider{name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded}

	outcome, err := newDispatcher(t, newRegistry(a, b)).
		runTaskIn(t, containerTask(t, &job.Retry{Attempts: new(3)}), nil, nil)
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("error = %v, want ErrExhausted", err)
	}

	if len(outcome.Attempts) != 1 {
		t.Errorf("made %d attempts, want 1", len(outcome.Attempts))
	}

	if b.submits != 0 {
		t.Error("a task without reroute reached a second provider")
	}
}

// A budget larger than the field is capped by it, rather than trying the same
// provider again for an outage it is still having.
func TestBudgetIsCappedByCandidates(t *testing.T) {
	t.Parallel()

	a := &scriptedProvider{name: "a", submitErr: plugin.Infrastructure(errors.New("503"))}

	outcome, err := newDispatcher(t, newRegistry(a)).
		runTaskIn(t, containerTask(t, reroutingRetry(5)), nil, nil)
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("error = %v, want ErrExhausted", err)
	}

	if len(outcome.Attempts) != 1 {
		t.Errorf("made %d attempts against one provider, want 1", len(outcome.Attempts))
	}
}

// -------------------------------------------------------------------------
// NOWHERE TO RUN
// -------------------------------------------------------------------------

// A task no provider admits fails with ErrNoCandidates and its rejections.
func TestNoCandidates(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})
	reg.inputs[0].Enabled = false

	outcome, err := newDispatcher(t, reg).
		runTaskIn(t, containerTask(t, nil), nil, nil)
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("error = %v, want ErrNoCandidates", err)
	}

	// The rejections are the actionable part, so they survive the error.
	if len(outcome.Rejections) == 0 {
		t.Error("no rejections were reported")
	}
}

// A ranking naming a provider the registry cannot produce is a disagreement
// between the two, not a provider outage.
func TestUnregisteredProvider(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})
	delete(reg.providers, "a")

	_, err := newDispatcher(t, reg).
		runTaskIn(t, containerTask(t, nil), nil, nil)
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("error = %v, want ErrNoProvider", err)
	}
}

// -------------------------------------------------------------------------
// QUOTA
// -------------------------------------------------------------------------

// refusingLedger refuses the providers a test names, standing in for quota
// another process spent between admission and dispatch.
type refusingLedger struct {
	*ledger.Ledger
	refuse map[string]bool
}

// Reserve refuses a named provider and reserves on the real ledger otherwise.
func (l *refusingLedger) Reserve(
	ctx context.Context, id execution.ID, namespace, provider string, e quota.Execution, pays bool,
) error {
	if l.refuse[provider] {
		return &ledger.Refusal{
			Provider: provider, Pool: "compute", Meter: quota.MeterGBSeconds,
			Limit: 1, Used: 1, Needed: 1,
		}
	}

	return l.Ledger.Reserve(ctx, id, namespace, provider, e, pays)
}

// brokenLedger cannot record anything.
type brokenLedger struct {
	*ledger.Ledger
}

// Reserve fails as a store that is down does.
func (brokenLedger) Reserve(context.Context, execution.ID, string, string, quota.Execution, bool) error {
	return errors.New("store down")
}

// noWait is a sleeper that returns at once.
func noWait(context.Context, time.Duration) error { return nil }

// The reservation is the declared timeout; the settled charge is what the run
// took.
func TestCompletedRunSettlesToWhatItCost(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	if _, err := newDispatcher(t, reg).
		runTaskIn(t, containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	// The scripted result reports one second.
	want := quota.FixtureContainer().Deltas(quota.Execution{CPU: 1000, Memory: 512, Duration: time.Second})
	usage := usageOf(reg, "a")

	for _, pool := range []string{"cpu", "compute"} {
		if usage[pool] != want[pool] {
			t.Errorf("%s = %d, want the settled %d", pool, usage[pool], want[pool])
		}
	}

	if got := usage["requests"]; got != 10_000+1 {
		t.Errorf("requests = %d, want one more than the 10000 already charged", got)
	}
}

// A cancelled run has no result, so it is charged from the start the provider
// reports to when it was cancelled, neither nothing nor what it reserved.
func TestCancelledRunIsChargedForWhatItRan(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 100, finalState: execution.StateSucceeded, startedAt: time.Now().Add(-2 * time.Minute),
	}
	reg := newRegistry(p)

	ctx, cancel := context.WithCancel(t.Context())
	d := over(reg, cancelling(p, cancel))

	o := begin(t, d)
	_, err := d.RunTask(ctx, o, containerTask(t, nil), nil, nil)
	d.Finish(t.Context(), o, nil, err)

	ran := quota.FixtureContainer().Deltas(quota.Execution{CPU: 1000, Memory: 512, Duration: 2 * time.Minute})
	reserved := quota.FixtureContainer().Deltas(quota.Execution{CPU: 1000, Memory: 512, Duration: 5 * time.Minute})

	if got := usageOf(reg, "a")["compute"]; got < ran["compute"] || got >= reserved["compute"] {
		t.Errorf("compute = %d, want the two minutes it ran: at least %d, under the reserved %d",
			got, ran["compute"], reserved["compute"])
	}
}

// A provider whose Cancel deletes the run no longer knows it afterwards; one
// seen running is still charged for that time, not nothing.
func TestCancelledRunTheProviderForgetsIsChargedForWhatItRan(t *testing.T) {
	t.Parallel()

	p, reg := answering(execution.StateSucceeded)
	p.pollsToFinish, p.forgetOnCancel = 100, true

	ctx, cancel := context.WithCancel(t.Context())
	d := over(reg, cancelling(&p.scriptedProvider, cancel))
	d.now = stepping(time.Minute)

	o := begin(t, d)
	_, err := d.RunTask(ctx, o, containerTask(t, nil), nil, nil)
	d.Finish(t.Context(), o, nil, err)

	if got := usageOf(reg, "a")["compute"]; got == 0 || p.cancels != 1 {
		t.Errorf("compute = %d after %d cancels, want the time seen running after one", got, p.cancels)
	}
}

// What the platform billed is a fact and supersedes the formula, even past the
// reservation.
func TestBilledRunSettlesToWhatThePlatformCharged(t *testing.T) {
	t.Parallel()

	billed := quota.Execution{Memory: 4096, Duration: 10 * time.Minute}

	p := &scriptedProvider{
		name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded, billed: &billed,
	}
	reg := newRegistry(p)

	if _, err := newDispatcher(t, reg).
		runTaskIn(t, containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	want := quota.FixtureContainer().Deltas(billed)

	if got := usageOf(reg, "a")["compute"]; got != want["compute"] {
		t.Errorf("compute = %d, want the billed %d", got, want["compute"])
	}
}

// Nothing says what a run that never answered cost, so once its grace period
// passes it is charged the declared worst case.
func TestLostRunIsChargedAsReserved(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", statusErr: plugin.Infrastructure(errors.New("503"))}
	reg := newRegistry(p)
	d := newDispatcher(t, reg)
	d.now = stepping(time.Hour)

	if _, err := d.runTaskIn(t, containerTask(t, nil), nil, nil); err == nil {
		t.Fatal("a provider that never answered reported success")
	}

	want := quota.FixtureContainer().Deltas(quota.Execution{CPU: 1000, Memory: 512, Duration: 5 * time.Minute})

	if got := usageOf(reg, "a")["compute"]; got != want["compute"] {
		t.Errorf("compute = %d, want the reserved %d", got, want["compute"])
	}
}

// A submission the provider refused ran nothing, so its reservation is given
// back in full rather than held for the reaper.
func TestRefusedSubmissionChargesNothing(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", submitErr: plugin.Internal(errors.New("400 bad request"))}
	reg := newRegistry(p)

	if _, err := newDispatcher(t, reg).
		runTaskIn(t, containerTask(t, nil), nil, nil); err == nil {
		t.Fatal("a refused submission reported success")
	}

	usage := usageOf(reg, "a")

	for _, pool := range []string{"cpu", "compute"} {
		if usage[pool] != 0 {
			t.Errorf("%s = %d, want 0 for a run that never existed", pool, usage[pool])
		}
	}

	if got := usage["requests"]; got != 10_000 {
		t.Errorf("requests = %d, want the 10000 already charged and no more", got)
	}
}

// A submission that failed in transit may have started a run, so the provider
// is asked after the cancel: one it never heard of is charged nothing, and
// one it cannot be asked about the declared worst case, whether the failure
// was an outage or one nobody classified.
func TestFailedSubmissionIsChargedForWhatRan(t *testing.T) {
	t.Parallel()

	reserved := quota.FixtureContainer().Deltas(quota.Execution{CPU: 1000, Memory: 512, Duration: 5 * time.Minute})

	tests := []struct {
		name      string
		submitErr error
		statusErr error
		want      int64
	}{
		{"never heard of it", plugin.Infrastructure(errors.New("503")), plugin.ErrUnknownExecution, 0},
		{"cannot be asked", plugin.Infrastructure(errors.New("503")), plugin.Infrastructure(errors.New("503")), reserved["compute"]},
		{"unclassified", errors.New("connection reset"), errors.New("connection reset"), reserved["compute"]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &scriptedProvider{name: "a", submitErr: tt.submitErr, statusErr: tt.statusErr}
			reg := newRegistry(p)

			if _, err := newDispatcher(t, reg).
				runTaskIn(t, containerTask(t, nil), nil, nil); err == nil {
				t.Fatal("a failed submission reported success")
			}

			if got := usageOf(reg, "a")["compute"]; got != tt.want || p.cancels != 1 {
				t.Errorf("compute = %d after %d cancels, want %d after one", got, p.cancels, tt.want)
			}
		})
	}
}

// A refusal sent nothing, so the task's single attempt is still there for the
// next provider.
func TestRefusedProviderDoesNotSpendAnAttempt(t *testing.T) {
	t.Parallel()

	first := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	second := &scriptedProvider{name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(first, second)

	led := &refusingLedger{Ledger: reg.ledger, refuse: map[string]bool{"a": true}}

	outcome, err := New(reg, led, reg.executions, WithSleeper(noWait)).
		runTaskIn(t, containerTask(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if first.submits != 0 {
		t.Errorf("submitted %d times to a refused provider", first.submits)
	}

	if outcome.Provider != "b" {
		t.Errorf("ran on %q, want b", outcome.Provider)
	}

	if len(outcome.Attempts) != 2 || !outcome.Attempts[0].Refused {
		t.Errorf("attempts = %+v, want the refusal recorded ahead of the run", outcome.Attempts)
	}

	if outcome.Rerouted() {
		t.Error("a refusal was reported as a reroute")
	}
}

// payingLedger records whether each reservation was made for a job that pays.
type payingLedger struct {
	*ledger.Ledger
	pays []bool
}

// Reserve notes pays and reserves as the real ledger does.
func (l *payingLedger) Reserve(
	ctx context.Context, id execution.ID, namespace, provider string, e quota.Execution, pays bool,
) error {
	l.pays = append(l.pays, pays)

	return l.Ledger.Reserve(ctx, id, namespace, provider, e, pays)
}

// A job that budgeted for paid capacity reserves as one that pays, so the
// ledger charges it past a spent allowance as admission already let it through.
func TestPayingJobReservesAsPaying(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		routing *job.Routing
		want    bool
	}{
		{name: "no budget", routing: nil, want: false},
		{name: "a budget", routing: &job.Routing{MaxCost: new(job.Cost(1))}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reg := newRegistry(&scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded})
			led := &payingLedger{Ledger: reg.ledger}

			if _, err := New(reg, led, reg.executions, WithSleeper(noWait)).
				runTaskIn(t, containerTask(t, nil), tc.routing, nil); err != nil {
				t.Fatalf("RunTask() = %v", err)
			}

			if len(led.pays) != 1 || led.pays[0] != tc.want {
				t.Errorf("reserved with pays = %v, want [%v]", led.pays, tc.want)
			}
		})
	}
}

// Admission saw quota that was gone by dispatch, so nothing was eligible after
// all.
func TestEveryProviderRefusedIsNoCandidates(t *testing.T) {
	t.Parallel()

	a := &scriptedProvider{name: "a"}
	b := &scriptedProvider{name: "b"}
	reg := newRegistry(a, b)

	led := &refusingLedger{Ledger: reg.ledger, refuse: map[string]bool{"a": true, "b": true}}

	_, err := New(reg, led, reg.executions, WithSleeper(noWait)).
		runTaskIn(t, containerTask(t, reroutingRetry(3)), nil, nil)
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("error = %v, want ErrNoCandidates", err)
	}

	if _, ok := errors.AsType[*ledger.Refusal](err); !ok {
		t.Errorf("error = %v, want it to carry the refusal", err)
	}

	if a.submits+b.submits != 0 {
		t.Error("something was submitted with every reservation refused")
	}
}

// A charge that could not be recorded is spending without a record, so nothing
// is dispatched on it and no other provider is tried.
func TestUnrecordableReservationDispatchesNothing(t *testing.T) {
	t.Parallel()

	a := &scriptedProvider{name: "a"}
	b := &scriptedProvider{name: "b"}
	reg := newRegistry(a, b)

	_, err := New(reg, brokenLedger{reg.ledger}, reg.executions, WithSleeper(noWait)).
		runTaskIn(t, containerTask(t, reroutingRetry(3)), nil, nil)
	if err == nil || errors.Is(err, ErrNoCandidates) {
		t.Fatalf("error = %v, want the ledger failure", err)
	}

	if a.submits+b.submits != 0 {
		t.Error("dispatched with no record of the charge")
	}
}

// -------------------------------------------------------------------------
// JOBS
// -------------------------------------------------------------------------

// A task that fails on its own answer stops the job before the next task.
func TestRunStopsAtTheFirstFailingTask(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 1, finalState: execution.StateFailed, exitCode: 2,
	}

	j := &job.Job{
		Name: "two-steps",
		Tasks: []job.Task{
			*containerTask(t, nil),
			*containerTask(t, nil),
		},
	}
	j.Tasks[0].Name = "first"
	j.Tasks[1].Name = "second"

	outcome, err := runJob(t, newDispatcher(t, newRegistry(p)), origin, j)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(outcome.Tasks) != 1 {
		t.Errorf("ran %d tasks, want 1", len(outcome.Tasks))
	}

	if outcome.Succeeded() {
		t.Error("a job whose first step failed reported success")
	}

	if p.submits != 1 {
		t.Errorf("submitted %d times, want 1", p.submits)
	}
}

// Every task of a passing job runs, in declaration order.
func TestRunExecutesEveryTaskInOrder(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}

	j := &job.Job{
		Name:  "two-steps",
		Tasks: []job.Task{*containerTask(t, nil), *containerTask(t, nil)},
	}
	j.Tasks[0].Name = "first"
	j.Tasks[1].Name = "second"

	outcome, err := runJob(t, newDispatcher(t, newRegistry(p)), origin, j)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !outcome.Succeeded() {
		t.Error("a job whose tasks all passed did not report success")
	}

	if got := []string{outcome.Tasks[0].Task, outcome.Tasks[1].Task}; got[0] != "first" ||
		got[1] != "second" {
		t.Errorf("tasks ran as %v, want declaration order", got)
	}
}

// The outcome carries what did run, so a caller can report it.
func TestRunReportsPartialProgress(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})
	delete(reg.providers, "a")

	j := &job.Job{Name: "j", Tasks: []job.Task{*containerTask(t, nil)}}

	outcome, err := runJob(t, newDispatcher(t, reg), origin, j)
	if err == nil {
		t.Fatal("expected an error")
	}

	if outcome == nil || outcome.Job != "j" {
		t.Fatalf("no outcome was returned alongside the error")
	}
}

// -------------------------------------------------------------------------
// CANCELLATION
// -------------------------------------------------------------------------

// A caller that gave up is not an outage to route around.
func TestCancelledContextStopsTheRun(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 100, finalState: execution.StateSucceeded}

	ctx, cancel := context.WithCancel(t.Context())

	d := over(newRegistry(p), WithSleeper(func(context.Context, time.Duration) error {
		cancel()

		return context.Canceled
	}))

	_, err := d.RunTask(ctx, begin(t, d), containerTask(t, nil), nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// A caller that gave up leaves work running and billing, so it is stopped.
func TestCancelledRunStopsTheExecution(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 100, finalState: execution.StateSucceeded,
	}

	ctx, cancel := context.WithCancel(t.Context())

	polls := 0

	d := over(newRegistry(p), WithSleeper(func(context.Context, time.Duration) error {
		polls++
		if polls > 1 {
			cancel()

			return context.Canceled
		}

		return nil
	}))

	o := begin(t, d)

	_, err := d.RunTask(ctx, o, containerTask(t, nil), nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}

	d.Finish(t.Context(), o, nil, err)

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

	d := newDispatcher(t, newRegistry(p))
	d.now = stepping(time.Hour)

	if _, err := d.runTaskIn(t, containerTask(t, nil), nil, nil); err == nil {
		t.Fatal("expected a failure")
	}

	if p.cancels != 0 {
		t.Errorf("called Cancel %d times on a provider failure, want 0", p.cancels)
	}
}
