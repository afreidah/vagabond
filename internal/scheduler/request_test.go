// -------------------------------------------------------------------------------
// Request Tests
//
// Author: Alex Freidah
//
// What the admission table cannot reach: deriving the image from an undecoded
// driver config, and the paid path, where a job that budgeted for capacity is
// admitted to a provider a free-only job is refused.
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
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

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

// The image is read out of a container task's driver config.
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

	req, diags := NewRequest(task, nil, "", nil)
	if diags.HasErrors() {
		t.Fatalf("building request failed: %s", diags.Error())
	}

	if req.Image != "golang:1.27" {
		t.Errorf("Image = %q, want golang:1.27", req.Image)
	}
}

// An image referencing job metadata is evaluated against the context, since
// the config block is left undecoded at parse time.
func TestNewRequestInterpolatesImage(t *testing.T) {
	t.Parallel()

	task := &job.Task{
		Name:   "test",
		Driver: job.DriverContainer,
		Config: configBlock(t, `image = "golang:${tag}"`),
	}

	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{"tag": cty.StringVal("1.27")},
	}

	req, diags := NewRequest(task, nil, "", ctx)
	if diags.HasErrors() {
		t.Fatalf("building request failed: %s", diags.Error())
	}

	if req.Image != "golang:1.27" {
		t.Errorf("Image = %q, want golang:1.27", req.Image)
	}
}

// A function task names a handler, not an image, so its request has none and
// a provider running only its own images does not reject it.
func TestNewRequestWithoutImage(t *testing.T) {
	t.Parallel()

	task := &job.Task{
		Name:   "test",
		Driver: job.DriverFunction,
		Config: configBlock(t, `handler = "main.handler"`),
	}

	req, diags := NewRequest(task, nil, "", nil)
	if diags.HasErrors() {
		t.Fatalf("building request failed: %s", diags.Error())
	}

	if req.Image != "" {
		t.Errorf("Image = %q, want empty", req.Image)
	}
}

// A task with no driver config builds a request with no image.
func TestNewRequestWithoutConfig(t *testing.T) {
	t.Parallel()

	req, diags := NewRequest(&job.Task{Name: "test"}, nil, "", nil)
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

// A job that stated no ceiling pays nothing; one that set a ceiling pays up
// to it.
func TestMaxCostDefaultsToFree(t *testing.T) {
	t.Parallel()

	req := baseRequest()

	if got := req.MaxCost(); got != 0 {
		t.Errorf("MaxCost() = %d, want 0", got)
	}

	if req.WillPay() {
		t.Error("a job with no routing will pay")
	}

	req.Routing = &job.Routing{MaxCost: new(job.Cost(5))}

	if got := req.MaxCost(); got != 5 {
		t.Errorf("MaxCost() = %d, want 5", got)
	}

	if !req.WillPay() {
		t.Error("a job that budgeted 5 will not pay")
	}
}

// A spent free tier refuses only a job that will not pay; a paying job is
// still admitted.
func TestPayingJobSurvivesAnExhaustedFreeTier(t *testing.T) {
	t.Parallel()

	spent := baseInput()
	setFreePercent(&spent, 0)

	free := baseRequest()
	if admitOnly(t, free, &spent).Admitted() {
		t.Error("a free-only job was admitted to a spent provider")
	}

	paying := baseRequest()
	paying.Routing = &job.Routing{MaxCost: new(job.Cost(5))}

	if !admitOnly(t, paying, &spent).Admitted() {
		t.Error("a job that budgeted for paid capacity was refused a spent provider")
	}
}

// A provider charging more than a paying job allows is still refused, so
// max_cost_usd is a ceiling rather than a waiver.
func TestPayingJobStillHasACeiling(t *testing.T) {
	t.Parallel()

	expensive := baseInput()
	expensive.Capabilities.EstimatedCost = 9

	req := baseRequest()
	req.Routing = &job.Routing{MaxCost: new(job.Cost(5))}

	result := admitOnly(t, req, &expensive)
	if result.Admitted() {
		t.Fatal("a provider charging 9 was admitted for a ceiling of 5")
	}

	if got := result.Rejections[0].Reason; got != ReasonCostPolicy {
		t.Errorf("reason = %s, want %s", got, ReasonCostPolicy)
	}
}

// -------------------------------------------------------------------------
// POOLS
// -------------------------------------------------------------------------

// A refusal names the pool, since "no quota" and "no compute quota, plenty of
// requests" have different fixes.
func TestQuotaRefusalNamesThePool(t *testing.T) {
	t.Parallel()

	spent := baseInput()
	setFreePercent(&spent, 0)

	rejection := admitOnly(t, baseRequest(), &spent).Rejections[0]
	if rejection.Reason != ReasonQuotaExhausted {
		t.Fatalf("reason = %s, want %s", rejection.Reason, ReasonQuotaExhausted)
	}

	if !strings.Contains(rejection.Detail, `Pool "requests"`) {
		t.Errorf("detail does not name the pool: %s", rejection.Detail)
	}
}

// An operator who declared no pools enforces nothing, so the provider is
// admitted rather than treated as unknown.
func TestProviderWithoutPoolsIsAdmitted(t *testing.T) {
	t.Parallel()

	unmetered := baseInput()
	unmetered.Limits = quota.Limits{}
	unmetered.Usage = nil

	if !admitOnly(t, baseRequest(), &unmetered).Admitted() {
		t.Error("a provider with no pools was refused")
	}
}

// -------------------------------------------------------------------------
// MALFORMED INPUT
// -------------------------------------------------------------------------

// A timeout that does not parse is passed over rather than blamed on a
// provider, since jobspec validation reports it.
func TestUnparseableTimeoutIsNotAProviderRejection(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	req.Task.Timeout = new(job.Duration("a fortnight"))

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

// A job's own tier mode wins over the server's, and with neither the mode is
// strict.
func TestRequestTiers(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		routing *job.Routing
		server  job.TierMode
		want    job.TierMode
	}{
		"neither":         {want: job.TiersStrict},
		"server":          {server: job.TiersWeighted, want: job.TiersWeighted},
		"job":             {routing: &job.Routing{Tiers: new(job.TiersWeighted)}, want: job.TiersWeighted},
		"job over server": {routing: &job.Routing{Tiers: new(job.TiersStrict)}, server: job.TiersWeighted, want: job.TiersStrict},
		"routing no mode": {routing: &job.Routing{}, server: job.TiersWeighted, want: job.TiersWeighted},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := &Request{Routing: tc.routing, ServerTiers: tc.server}
			if got := req.Tiers(); got != tc.want {
				t.Errorf("Tiers() = %q, want %q", got, tc.want)
			}
		})
	}
}
