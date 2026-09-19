// -------------------------------------------------------------------------------
// Request Tests
//
// Author: Alex Freidah
//
// The accessors are mostly exercised by the admission table, so what is here is
// the two things that table cannot reach: deriving the image out of an
// undecoded driver config, and the paid path, where a job that budgeted for
// capacity is admitted to a provider a free-only job would be refused.
// -------------------------------------------------------------------------------

package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
)

// configBlock parses a driver config the way the job parser leaves it: decoded
// as a block, with its attributes still expressions.
func configBlock(t *testing.T, src string) *job.RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing config failed: %s", diags.Error())
	}

	return &job.RawBlock{Body: f.Body}
}

// -------------------------------------------------------------------------
// DERIVATION
// -------------------------------------------------------------------------

func TestNewRequestDerivesImage(t *testing.T) {
	t.Parallel()

	task := &job.Task{
		Name:   "test",
		Driver: job.DriverContainer,
		Config: configBlock(t, `
image   = "golang:1.27"
command = "go test ./..."
`),
	}

	req, diags := NewRequest(task, nil, nil)
	if diags.HasErrors() {
		t.Fatalf("building request failed: %s", diags.Error())
	}

	if req.Image != "golang:1.27" {
		t.Errorf("Image = %q, want golang:1.27", req.Image)
	}
}

func TestNewRequestInterpolatesImage(t *testing.T) {
	t.Parallel()

	// The config block is left undecoded at parse time, so an image tagged with
	// job metadata is still an expression when admission needs its value.
	task := &job.Task{
		Name:   "test",
		Driver: job.DriverContainer,
		Config: configBlock(t, `image = "golang:${tag}"`),
	}

	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{"tag": cty.StringVal("1.27")},
	}

	req, diags := NewRequest(task, nil, ctx)
	if diags.HasErrors() {
		t.Fatalf("building request failed: %s", diags.Error())
	}

	if req.Image != "golang:1.27" {
		t.Errorf("Image = %q, want golang:1.27", req.Image)
	}
}

func TestNewRequestWithoutImage(t *testing.T) {
	t.Parallel()

	// A function task names a handler, not an image, and must not be rejected
	// by a provider that runs only its own.
	task := &job.Task{
		Name:   "test",
		Driver: job.DriverFunction,
		Config: configBlock(t, `handler = "main.handler"`),
	}

	req, diags := NewRequest(task, nil, nil)
	if diags.HasErrors() {
		t.Fatalf("building request failed: %s", diags.Error())
	}

	if req.Image != "" {
		t.Errorf("Image = %q, want empty", req.Image)
	}
}

func TestNewRequestWithoutConfig(t *testing.T) {
	t.Parallel()

	req, diags := NewRequest(&job.Task{Name: "test"}, nil, nil)
	if diags.HasErrors() {
		t.Fatalf("building request failed: %s", diags.Error())
	}

	if req.Image != "" {
		t.Errorf("Image = %q, want empty", req.Image)
	}
}

// -------------------------------------------------------------------------
// PAYING
// -------------------------------------------------------------------------

// A job that stated no ceiling gets the one this project exists to enforce.
// Free is the default and paying is the deliberate act, the same way disabling
// a provider is.
func TestMaxCostDefaultsToFree(t *testing.T) {
	t.Parallel()

	req := baseRequest()

	if got := req.MaxCost(); got != 0 {
		t.Errorf("MaxCost() = %d, want 0", got)
	}

	if req.WillPay() {
		t.Error("a job with no routing will pay")
	}

	req.Routing = &job.Routing{MaxCost: ptr.Of(job.Cost(5))}

	if got := req.MaxCost(); got != 5 {
		t.Errorf("MaxCost() = %d, want 5", got)
	}

	if !req.WillPay() {
		t.Error("a job that budgeted 5 will not pay")
	}
}

// An exhausted free tier is not a provider being unable to run the work, it is
// the work costing money from here on. Only a job that will not pay is refused
// for it.
func TestPayingJobSurvivesAnExhaustedFreeTier(t *testing.T) {
	t.Parallel()

	spent := baseInput()
	spent.Quota.Exhausted = true

	free := baseRequest()
	if admitOnly(t, free, &spent).Admitted() {
		t.Error("a free-only job was admitted to a spent provider")
	}

	paying := baseRequest()
	paying.Routing = &job.Routing{MaxCost: ptr.Of(job.Cost(5))}

	if !admitOnly(t, paying, &spent).Admitted() {
		t.Error("a job that budgeted for paid capacity was refused a spent provider")
	}
}

// Budgeting is not unlimited. A provider charging more than the job allows is
// still refused, which is what keeps max_cost_usd a ceiling rather than a
// waiver.
func TestPayingJobStillHasACeiling(t *testing.T) {
	t.Parallel()

	expensive := baseInput()
	expensive.Capabilities.EstimatedCost = 9

	req := baseRequest()
	req.Routing = &job.Routing{MaxCost: ptr.Of(job.Cost(5))}

	result := admitOnly(t, req, &expensive)
	if result.Admitted() {
		t.Fatal("a provider charging 9 was admitted for a ceiling of 5")
	}

	if got := result.Rejections[0].Reason; got != ReasonCostPolicy {
		t.Errorf("reason = %s, want %s", got, ReasonCostPolicy)
	}
}

// -------------------------------------------------------------------------
// UNOBSERVED QUOTA
// -------------------------------------------------------------------------

// A provider nothing is known about has no headroom, and says so differently
// from one that is merely spent: the fix for the first is a refresh and for the
// second it is waiting.
func TestUnobservedQuotaIsRefusedByName(t *testing.T) {
	t.Parallel()

	unobserved := baseInput()
	unobserved.Quota.ObservedAt = time.Time{}

	result := admitOnly(t, baseRequest(), &unobserved)

	rejection := result.Rejections[0]
	if rejection.Reason != ReasonQuotaExhausted {
		t.Fatalf("reason = %s, want %s", rejection.Reason, ReasonQuotaExhausted)
	}

	if !strings.Contains(rejection.Detail, "Nothing is known") {
		t.Errorf("an unobserved provider reads as a spent one: %s", rejection.Detail)
	}
}

// -------------------------------------------------------------------------
// MALFORMED INPUT
// -------------------------------------------------------------------------

// A timeout that does not parse is a job file error, reported by jobspec
// validation against the line the author wrote. Admission passing it over is
// what keeps a plan from blaming a provider for it.
func TestUnparseableTimeoutIsNotAProviderRejection(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	req.Task.Timeout = ptr.Of(job.Duration("a fortnight"))

	in := baseInput()
	in.Capabilities.MaxDuration = time.Minute

	if !admitOnly(t, req, &in).Admitted() {
		t.Error("a malformed timeout was reported as a provider limit")
	}
}

// A constraint on an attribute a provider simply did not publish reads
// differently from one it published empty, because the fixes differ.
func TestConstraintDetailNamesAnAbsentAttribute(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	req.Routing = &job.Routing{Constraints: []job.Constraint{{
		Attribute: plugin.AttrMaxDuration,
		Operator:  job.OperatorGreaterEqual,
		Value:     "20m",
	}}}

	in := baseInput()

	result := admitOnly(t, req, &in)

	rejection := result.Rejections[0]
	if rejection.Reason != ReasonConstraintUnmet {
		t.Fatalf("reason = %s, want %s", rejection.Reason, ReasonConstraintUnmet)
	}

	if !strings.Contains(rejection.Detail, "nothing for it") {
		t.Errorf("detail does not say the attribute is unpublished: %s", rejection.Detail)
	}
}
