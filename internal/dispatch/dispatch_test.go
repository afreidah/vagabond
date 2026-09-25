// -------------------------------------------------------------------------------
// Dispatch Tests
//
// Author: Alex Freidah
//
// The interesting behaviour here is what happens when a provider does not
// answer, so most of these scenarios are failures. The shared fake in
// internal/plugin cannot express them: it advances only when a test tells it
// to, which is right for a registry test and useless for one that has to watch
// a provider fail twice and recover on the third attempt.
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
	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// -------------------------------------------------------------------------
// DOUBLES
// -------------------------------------------------------------------------

// scriptedProvider answers however a test needs it to.
type scriptedProvider struct {
	name string

	// Failures, in order of the call that returns them.
	submitErr error
	statusErr error
	resultErr error

	// pollsToFinish is how many Status calls happen before a terminal state.
	pollsToFinish int
	finalState    execution.State
	exitCode      int

	// synchronous providers finish inside Submit, like a function invocation.
	synchronous bool

	// billed is what the platform reports it charged, nil for most.
	billed *quota.Execution

	submits int
	polls   int
	results int
	cancels int
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.FixtureContainer(time.Now()), nil
}

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
			Result:     &execution.Result{ID: id, ExitCode: ptr.Of(p.exitCode)},
		}, nil
	}

	return plugin.Submission{
		ProviderID: p.name + "-" + id.String(),
		State:      execution.StateAccepted,
	}, nil
}

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

	return execution.Status{ID: id, State: state}, nil
}

func (p *scriptedProvider) Result(
	_ context.Context, id execution.ID,
) (*execution.Result, error) {
	p.results++

	if p.resultErr != nil {
		return nil, p.resultErr
	}

	return &execution.Result{
		ID:       id,
		ExitCode: ptr.Of(p.exitCode),
		Duration: time.Second,
		Billed:   p.billed,
		Logs:     []byte("scripted output\n"),
	}, nil
}

func (p *scriptedProvider) Cancel(context.Context, execution.ID) error {
	p.cancels++

	return nil
}

// fakeRegistry holds whatever providers a test declared, and the ledger they
// are charged against.
type fakeRegistry struct {
	inputs    []scheduler.Input
	providers map[string]plugin.Provider
	ledger    *ledger.Ledger
}

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

// ns is the namespace every dispatch here runs in. It declares no shares.
const ns = "default"

// usageOf reads a provider's total usage from the registry's ledger.
func usageOf(reg *fakeRegistry, provider string) quota.PoolUsage {
	usage, _ := reg.ledger.PoolUsage(ns, provider)

	return usage
}

func (r *fakeRegistry) Provider(name string) (plugin.Provider, bool) {
	p, ok := r.providers[name]

	return p, ok
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// newRegistry registers providers, each healthy with container budgets.
//
// Each is charged a little more of its request pool than the one before, so
// free quota descends, ranking order matches declaration order, and a test can
// say "the second one" and mean it.
func newRegistry(providers ...*scriptedProvider) *fakeRegistry {
	reg := &fakeRegistry{providers: map[string]plugin.Provider{}}

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
	return New(reg, reg.ledger, opts...)
}

// newDispatcher builds one that never actually waits.
func newDispatcher(t *testing.T, reg *fakeRegistry) *Dispatcher {
	t.Helper()

	return over(reg, WithSleeper(func(context.Context, time.Duration) error {
		return nil
	}))
}

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
		Resources: &job.Resources{CPU: ptr.Of(1000), Memory: ptr.Of(512)},
		Timeout:   ptr.Of(job.Duration("5m")),
		Retry:     retry,
	}
}

// reroutingRetry allows n attempts across providers.
func reroutingRetry(n int) *job.Retry {
	return &job.Retry{Attempts: ptr.Of(n), Reroute: ptr.Of(true)}
}

// -------------------------------------------------------------------------
// THE HAPPY PATH
// -------------------------------------------------------------------------

func TestRunTaskPollsUntilTerminal(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{
		name: "a", pollsToFinish: 3, finalState: execution.StateSucceeded,
	}

	outcome, err := newDispatcher(t, newRegistry(p)).
		RunTask(t.Context(), ns, containerTask(t, nil), nil, nil)
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
		RunTask(t.Context(), ns, containerTask(t, nil), nil, nil)
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
		RunTask(t.Context(), ns, containerTask(t, reroutingRetry(3)), nil, nil)
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
		RunTask(t.Context(), ns, containerTask(t, reroutingRetry(2)), nil, nil)
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	if outcome.Provider != "b" {
		t.Errorf("answered by %q, want b", outcome.Provider)
	}

	if !outcome.Rerouted() {
		t.Error("the reroute was not recorded")
	}

	// The abandoned attempt keeps its failure, which is what explains the
	// reroute to whoever reads the outcome.
	if outcome.Attempts[0].Err == nil {
		t.Error("the first attempt recorded no error")
	}
}

// Our own bug reproduces everywhere, so sending it onward spends capacity to
// fail identically.
func TestInternalFailureDoesNotReroute(t *testing.T) {
	t.Parallel()

	first := &scriptedProvider{
		name: "a", submitErr: plugin.Internal(errors.New("malformed request")),
	}
	second := &scriptedProvider{
		name: "b", pollsToFinish: 1, finalState: execution.StateSucceeded,
	}

	_, err := newDispatcher(t, newRegistry(first, second)).
		RunTask(t.Context(), ns, containerTask(t, reroutingRetry(3)), nil, nil)
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
		RunTask(t.Context(), ns, containerTask(t, reroutingRetry(2)), nil, nil)
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
		RunTask(t.Context(), ns, containerTask(t, nil), nil, nil)
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
		RunTask(t.Context(), ns, containerTask(t, &job.Retry{Attempts: ptr.Of(3)}), nil, nil)
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
		RunTask(t.Context(), ns, containerTask(t, reroutingRetry(5)), nil, nil)
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

func TestNoCandidates(t *testing.T) {
	t.Parallel()

	reg := newRegistry(&scriptedProvider{name: "a"})
	reg.inputs[0].Enabled = false

	outcome, err := newDispatcher(t, reg).
		RunTask(t.Context(), ns, containerTask(t, nil), nil, nil)
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
		RunTask(t.Context(), ns, containerTask(t, nil), nil, nil)
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

func (l *refusingLedger) Reserve(
	ctx context.Context, id execution.ID, namespace, provider string, e quota.Execution,
) error {
	if l.refuse[provider] {
		return &ledger.Refusal{
			Provider: provider, Pool: "compute", Meter: quota.MeterGBSeconds,
			Limit: 1, Used: 1, Needed: 1,
		}
	}

	return l.Ledger.Reserve(ctx, id, namespace, provider, e)
}

// brokenLedger cannot record anything.
type brokenLedger struct {
	*ledger.Ledger
}

func (brokenLedger) Reserve(context.Context, execution.ID, string, string, quota.Execution) error {
	return errors.New("store down")
}

func noWait(context.Context, time.Duration) error { return nil }

// The reservation is the declared timeout; the settled charge is what the run
// took.
func TestCompletedRunSettlesToWhatItCost(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}
	reg := newRegistry(p)

	if _, err := newDispatcher(t, reg).
		RunTask(t.Context(), ns, containerTask(t, nil), nil, nil); err != nil {
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
		RunTask(t.Context(), ns, containerTask(t, nil), nil, nil); err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}

	want := quota.FixtureContainer().Deltas(billed)

	if got := usageOf(reg, "a")["compute"]; got != want["compute"] {
		t.Errorf("compute = %d, want the billed %d", got, want["compute"])
	}
}

// Nothing says what a run that never answered cost, so it stays charged at the
// declared worst case.
func TestLostRunKeepsItsReservation(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", statusErr: plugin.Infrastructure(errors.New("503"))}
	reg := newRegistry(p)

	if _, err := newDispatcher(t, reg).
		RunTask(t.Context(), ns, containerTask(t, nil), nil, nil); err == nil {
		t.Fatal("a provider that never answered reported success")
	}

	want := quota.FixtureContainer().Deltas(quota.Execution{CPU: 1000, Memory: 512, Duration: 5 * time.Minute})

	if got := usageOf(reg, "a")["compute"]; got != want["compute"] {
		t.Errorf("compute = %d, want the reserved %d", got, want["compute"])
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

	outcome, err := New(reg, led, WithSleeper(noWait)).
		RunTask(t.Context(), ns, containerTask(t, nil), nil, nil)
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

// Admission saw quota that was gone by dispatch, so nothing was eligible after
// all.
func TestEveryProviderRefusedIsNoCandidates(t *testing.T) {
	t.Parallel()

	a := &scriptedProvider{name: "a"}
	b := &scriptedProvider{name: "b"}
	reg := newRegistry(a, b)

	led := &refusingLedger{Ledger: reg.ledger, refuse: map[string]bool{"a": true, "b": true}}

	_, err := New(reg, led, WithSleeper(noWait)).
		RunTask(t.Context(), ns, containerTask(t, reroutingRetry(3)), nil, nil)
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("error = %v, want ErrNoCandidates", err)
	}

	var refusal *ledger.Refusal
	if !errors.As(err, &refusal) {
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

	_, err := New(reg, brokenLedger{reg.ledger}, WithSleeper(noWait)).
		RunTask(t.Context(), ns, containerTask(t, reroutingRetry(3)), nil, nil)
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

	outcome, err := newDispatcher(t, newRegistry(p)).Run(t.Context(), ns, j, nil)
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

func TestRunExecutesEveryTaskInOrder(t *testing.T) {
	t.Parallel()

	p := &scriptedProvider{name: "a", pollsToFinish: 1, finalState: execution.StateSucceeded}

	j := &job.Job{
		Name:  "two-steps",
		Tasks: []job.Task{*containerTask(t, nil), *containerTask(t, nil)},
	}
	j.Tasks[0].Name = "first"
	j.Tasks[1].Name = "second"

	outcome, err := newDispatcher(t, newRegistry(p)).Run(t.Context(), ns, j, nil)
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

	outcome, err := newDispatcher(t, reg).Run(t.Context(), ns, j, nil)
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

	_, err := d.RunTask(ctx, ns, containerTask(t, nil), nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
