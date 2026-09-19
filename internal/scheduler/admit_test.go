// -------------------------------------------------------------------------------
// Admission Tests
//
// Author: Alex Freidah
//
// Every rejection reason has to be reachable, and the table below is what
// proves it: a baseline that every checker admits, and one case per reason that
// changes exactly one thing. A reason that stops being producible fails here
// rather than quietly becoming vocabulary nothing emits.
//
// Nothing constructs a provider. Admission is a pure function over snapshots,
// and a test that needed a plugin to reach a verdict would mean it is not.
// -------------------------------------------------------------------------------

package scheduler

import (
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/quota"
)

// observed is a fixed point in time, so that a snapshot counts as observed
// without any test depending on when it ran.
var observed = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// baseRequest is a task every baseline provider can run: one container, no
// architecture, no limits, no image, and no routing policy at all.
func baseRequest() *Request {
	return &Request{
		Task: &job.Task{
			Name:   "build",
			Driver: job.DriverContainer,
		},
	}
}

// baseInput is a provider that admits the baseline request. Each table case
// changes one field, so whatever it produces is attributable to that field.
func baseInput() Input {
	return Input{
		Provider: "test",
		Capabilities: plugin.Capabilities{
			Drivers:         []job.DriverName{job.DriverContainer},
			Architectures:   []job.Arch{job.ArchAMD64},
			InternetEgress:  true,
			PrivateNetwork:  true,
			ArbitraryImages: true,
			ObservedAt:      observed,
		},
		Quota: quota.Snapshot{
			Provider:    "test",
			FreePercent: 50,
			ObservedAt:  observed,
		},
		Enabled: true,
		Healthy: true,
	}
}

// admitOnly runs admission against a single provider and returns the verdict.
func admitOnly(t *testing.T, req *Request, in *Input) *Result {
	t.Helper()

	result := Admit(req, []Input{*in})

	return &result
}

// applyCase builds the request and provider one table case describes.
func (tc reasonCase) apply() (*Request, Input) {
	req := baseRequest()
	if tc.request != nil {
		tc.request(req)
	}

	in := baseInput()
	if tc.input != nil {
		tc.input(&in)
	}

	return req, in
}

// -------------------------------------------------------------------------
// BASELINE
// -------------------------------------------------------------------------

func TestAdmitBaseline(t *testing.T) {
	t.Parallel()

	result := admitOnly(t, baseRequest(), ptr.Of(baseInput()))

	if !result.Admitted() {
		t.Fatalf("baseline was rejected: %+v", result.Rejections)
	}

	if len(result.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(result.Candidates))
	}

	if got := result.Candidates[0].Provider; got != "test" {
		t.Errorf("candidate provider = %q, want test", got)
	}
}

// -------------------------------------------------------------------------
// EVERY REASON
// -------------------------------------------------------------------------

// reasonCase changes one thing about the baseline and names the reason that
// change should produce.
type reasonCase struct {
	request func(*Request)
	input   func(*Input)
	want    Reason
}

// reasonCases is shared by the table test and the coverage test, so that
// retiring a case cannot leave a reason silently unproduced.
var reasonCases = map[string]reasonCase{
	"job routes elsewhere": {
		request: func(r *Request) {
			r.Routing = &job.Routing{Providers: []string{"somewhere-else"}}
		},
		want: ReasonNotAllowlisted,
	},
	"provider charges and job will not pay": {
		input: func(in *Input) { in.Capabilities.EstimatedCost = 5 },
		want:  ReasonCostPolicy,
	},
	"driver not offered": {
		input: func(in *Input) {
			in.Capabilities.Drivers = []job.DriverName{job.DriverFunction}
		},
		want: ReasonDriverUnsupported,
	},
	"architecture not offered": {
		request: func(r *Request) {
			r.Task.Execution = &job.ExecutionRequirements{
				Architecture: ptr.Of(job.ArchARM64),
			}
		},
		want: ReasonArchUnsupported,
	},
	"image the provider will not run": {
		request: func(r *Request) { r.Image = "golang:1.27" },
		input:   func(in *Input) { in.Capabilities.ArbitraryImages = false },
		want:    ReasonImageUnsupported,
	},
	"private networking unavailable": {
		request: func(r *Request) {
			r.Task.Network = &job.Network{Private: ptr.Of(true)}
		},
		input: func(in *Input) { in.Capabilities.PrivateNetwork = false },
		want:  ReasonNetworkUnsupported,
	},
	"egress unavailable": {
		request: func(r *Request) {
			r.Task.Network = &job.Network{Internet: ptr.Of(true)}
		},
		input: func(in *Input) { in.Capabilities.InternetEgress = false },
		want:  ReasonNetworkUnsupported,
	},
	"more memory than the provider allows": {
		request: func(r *Request) {
			r.Task.Resources = &job.Resources{Memory: ptr.Of(4096)}
		},
		input: func(in *Input) {
			in.Capabilities.MaxResources = plugin.Resources{Memory: 512}
		},
		want: ReasonResourcesExceeded,
	},
	"more cpu than the provider allows": {
		request: func(r *Request) {
			r.Task.Resources = &job.Resources{CPU: ptr.Of(4000)}
		},
		input: func(in *Input) {
			in.Capabilities.MaxResources = plugin.Resources{CPU: 1800}
		},
		want: ReasonResourcesExceeded,
	},
	"longer than the provider allows": {
		request: func(r *Request) {
			r.Task.Timeout = ptr.Of(job.Duration("20m"))
		},
		input: func(in *Input) {
			in.Capabilities.MaxDuration = 15 * time.Minute
		},
		want: ReasonDurationExceeded,
	},
	"constraint names an attribute nobody publishes": {
		request: func(r *Request) {
			r.Routing = &job.Routing{Constraints: []job.Constraint{{
				Attribute: "provider.architekture",
				Operator:  job.OperatorSetContains,
				Value:     "amd64",
			}}}
		},
		want: ReasonAttributeUnknown,
	},
	"constraint not satisfied": {
		request: func(r *Request) {
			r.Routing = &job.Routing{Constraints: []job.Constraint{{
				Attribute: plugin.AttrArchitecture,
				Operator:  job.OperatorSetContains,
				Value:     "arm64",
			}}}
		},
		want: ReasonConstraintUnmet,
	},
	"operator turned it off": {
		input: func(in *Input) { in.Enabled = false },
		want:  ReasonProviderDisabled,
	},
	"provider is not answering": {
		input: func(in *Input) { in.Healthy = false },
		want:  ReasonUnhealthy,
	},
	"free tier is spent": {
		input: func(in *Input) { in.Quota.Exhausted = true },
		want:  ReasonQuotaExhausted,
	},
}

// Together the cases are the proof that no reason in the vocabulary is
// unreachable.
func TestAdmitProducesEveryReason(t *testing.T) {
	t.Parallel()

	for name, tc := range reasonCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req, in := tc.apply()

			result := admitOnly(t, req, &in)
			if result.Admitted() {
				t.Fatalf("provider was admitted, want %s", tc.want)
			}

			assertRejection(t, &result.Rejections[0], tc.want)
		})
	}
}

// assertRejection checks what every rejection owes a reader: the code a machine
// branches on, the provider it belongs to, and prose carrying the numbers.
func assertRejection(t *testing.T, got *Rejection, want Reason) {
	t.Helper()

	if got.Reason != want {
		t.Errorf("reason = %s, want %s (also: %v)", got.Reason, want, got.Also)
	}

	if got.Provider != "test" {
		t.Errorf("rejection provider = %q, want test", got.Provider)
	}

	if got.Detail == "" {
		t.Error("rejection carries no detail for a person to read")
	}
}

// Every reason the vocabulary declares has to be claimed by a case above.
// Without this, adding a reason and never emitting it would go unnoticed, which
// is the failure the reason codes exist to prevent.
func TestEveryReasonIsCovered(t *testing.T) {
	t.Parallel()

	claimed := make(map[Reason]bool, len(reasonCases))
	for _, tc := range reasonCases {
		claimed[tc.want] = true
	}

	for _, reason := range Reasons() {
		if !claimed[reason] {
			t.Errorf("no case produces %s", reason)
		}
	}
}

// -------------------------------------------------------------------------
// COLLECTING EVERY REASON
// -------------------------------------------------------------------------

// A provider that fails several checks reports all of them, so that fixing the
// first does not simply reveal the second on the next run.
func TestAdmitCollectsEveryReason(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	req.Task.Timeout = ptr.Of(job.Duration("20m"))
	req.Image = "golang:1.27"

	in := baseInput()
	in.Capabilities = plugin.FixtureWorker(observed)
	in.Quota.Exhausted = true

	result := admitOnly(t, req, &in)

	rejection := result.Rejections[0]

	if rejection.Reason != ReasonDriverUnsupported {
		t.Errorf("first reason = %s, want %s", rejection.Reason, ReasonDriverUnsupported)
	}

	want := []Reason{
		ReasonImageUnsupported,
		ReasonDurationExceeded,
		ReasonQuotaExhausted,
	}

	if diff := cmp.Diff(want, rejection.Also); diff != "" {
		t.Errorf("collected reasons mismatch (-want +got):\n%s", diff)
	}
}

// The first rejection is the only one carrying prose, because it is the only
// one the plan table renders.
func TestAdmitDetailsOnlyTheFirst(t *testing.T) {
	t.Parallel()

	req := baseRequest()

	in := baseInput()
	in.Enabled = false
	in.Healthy = false

	rejection := admitOnly(t, req, &in).Rejections[0]

	if rejection.Detail == "" {
		t.Error("the rendered rejection carries no detail")
	}

	if diff := cmp.Diff([]Reason{ReasonUnhealthy}, rejection.Also); diff != "" {
		t.Errorf("collected reasons mismatch (-want +got):\n%s", diff)
	}
}

// -------------------------------------------------------------------------
// THE THREE FIXTURES
// -------------------------------------------------------------------------

// A real container task against all three execution families. This is the case
// the fixtures were written in Chunk 1 to be compared against.
func TestAdmitContainerTaskAcrossFixtures(t *testing.T) {
	t.Parallel()

	req := &Request{
		Task: &job.Task{
			Name:      "test",
			Driver:    job.DriverContainer,
			Timeout:   ptr.Of(job.Duration("20m")),
			Resources: &job.Resources{CPU: ptr.Of(2000), Memory: ptr.Of(2048)},
			Execution: &job.ExecutionRequirements{Architecture: ptr.Of(job.ArchAMD64)},
			Network:   &job.Network{Internet: ptr.Of(true)},
		},
		Image: "golang:1.27",
	}

	inputs := []Input{
		fixtureInput("code-engine", plugin.FixtureContainer),
		fixtureInput("lambda", plugin.FixtureFunction),
		fixtureInput("workers", plugin.FixtureWorker),
	}

	result := Admit(req, inputs)

	// Only the container family can run a general CI task.
	if diff := cmp.Diff([]string{"code-engine"}, candidateNames(&result)); diff != "" {
		t.Errorf("candidates mismatch (-want +got):\n%s", diff)
	}

	rejections := map[string]Rejection{}
	for _, r := range result.Rejections {
		rejections[r.Provider] = r
	}

	// Both refuse on the driver, which is the most explanatory thing either can
	// say, and the collected reasons are where they differ.
	for _, name := range []string{"lambda", "workers"} {
		if got := rejections[name].Reason; got != ReasonDriverUnsupported {
			t.Errorf("%s: reason = %s, want %s", name, got, ReasonDriverUnsupported)
		}
	}

	// Lambda offers both architectures, so it fails on the image, its fifteen
	// minute limit, and its CPU ceiling. Workers offers no architecture at all.
	lambda := rejections["lambda"].Also
	if slices.Contains(lambda, ReasonArchUnsupported) {
		t.Errorf("lambda rejected on architecture it offers: %v", lambda)
	}

	workers := rejections["workers"].Also
	if !slices.Contains(workers, ReasonArchUnsupported) {
		t.Errorf("workers did not reject on architecture: %v", workers)
	}

	for _, want := range []Reason{ReasonImageUnsupported, ReasonDurationExceeded} {
		if !slices.Contains(lambda, want) {
			t.Errorf("lambda did not report %s: %v", want, lambda)
		}
	}
}

// fixtureInput wraps a capability fixture in an otherwise healthy provider.
//
// Takes the fixture rather than its result, so that a call site reads as the
// family it is standing up and no 112 byte snapshot is copied to get there.
func fixtureInput(name string, fixture func(time.Time) plugin.Capabilities) Input {
	return Input{
		Provider:     name,
		Capabilities: fixture(observed),
		Quota: quota.Snapshot{
			Provider:    name,
			FreePercent: 50,
			ObservedAt:  observed,
		},
		Enabled: true,
		Healthy: true,
	}
}

// candidateNames returns the admitted providers in result order.
func candidateNames(r *Result) []string {
	names := make([]string, 0, len(r.Candidates))
	for i := range r.Candidates {
		names = append(names, r.Candidates[i].Provider)
	}

	return names
}

// -------------------------------------------------------------------------
// DETERMINISM
// -------------------------------------------------------------------------

// Plan output has to be diffable in CI, which it is not if the order depends on
// how providers happened to be configured.
func TestAdmitIsOrderedByProvider(t *testing.T) {
	t.Parallel()

	req := baseRequest()

	inputs := []Input{
		fixtureInput("zulu", plugin.FixtureContainer),
		fixtureInput("alpha", plugin.FixtureContainer),
		fixtureInput("mike", plugin.FixtureFunction),
		fixtureInput("bravo", plugin.FixtureFunction),
	}

	result := Admit(req, inputs)

	if diff := cmp.Diff([]string{"alpha", "zulu"}, candidateNames(&result)); diff != "" {
		t.Errorf("candidates mismatch (-want +got):\n%s", diff)
	}

	var rejected []string
	for _, r := range result.Rejections {
		rejected = append(rejected, r.Provider)
	}

	if diff := cmp.Diff([]string{"bravo", "mike"}, rejected); diff != "" {
		t.Errorf("rejections mismatch (-want +got):\n%s", diff)
	}
}

// Admission must not change what it was handed. It runs against snapshots the
// registry holds and reuses, so a checker that sorted a slice in place would
// change what every later plan sees.
func TestAdmitDoesNotMutateInputs(t *testing.T) {
	t.Parallel()

	inputs := []Input{fixtureInput("code-engine", plugin.FixtureContainer)}
	before := inputs[0].Capabilities.Clone()

	result := Admit(baseRequest(), inputs)

	if diff := cmp.Diff(before, inputs[0].Capabilities); diff != "" {
		t.Errorf("admission changed its input (-before +after):\n%s", diff)
	}

	// The candidate carries its own copy, so a caller sorting it changes
	// nothing the registry still holds.
	result.Candidates[0].Capabilities.Drivers[0] = job.DriverWorker

	if inputs[0].Capabilities.Drivers[0] != job.DriverContainer {
		t.Error("a candidate shares its driver slice with the input")
	}
}

// -------------------------------------------------------------------------
// ORDER
// -------------------------------------------------------------------------

// The order is the design, so it is asserted rather than left to whoever edits
// the slice next.
func TestCheckerOrder(t *testing.T) {
	t.Parallel()

	want := []string{
		"allowlist", "cost",
		"enabled", "healthy",
		"driver", "arch", "image", "network", "resources", "duration",
		"attribute", "constraint",
		"quota",
	}

	got := make([]string, 0, len(want))
	for _, checker := range Checkers() {
		got = append(got, checker.Name())
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("checker order mismatch (-want +got):\n%s", diff)
	}

	// One checker per reason, so no reason is unreachable and none is produced
	// by a rule with no name of its own.
	if len(got) != len(Reasons()) {
		t.Errorf("%d checkers for %d reasons", len(got), len(Reasons()))
	}
}

// -------------------------------------------------------------------------
// RETRYABILITY
// -------------------------------------------------------------------------

// The reason a caller waits rather than edits has to survive being collected
// alongside permanent ones.
func TestRetryable(t *testing.T) {
	t.Parallel()

	spent := baseInput()
	spent.Quota.Exhausted = true

	onQuota := Admit(baseRequest(), []Input{spent})
	if !onQuota.Retryable() {
		t.Error("a job refused only on quota is not retryable")
	}

	mismatched := baseInput()
	mismatched.Capabilities.Drivers = []job.DriverName{job.DriverWorker}

	onDriver := Admit(baseRequest(), []Input{mismatched})
	if onDriver.Retryable() {
		t.Error("a job refused on driver support is retryable")
	}
}
