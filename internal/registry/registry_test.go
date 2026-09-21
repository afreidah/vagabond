// -------------------------------------------------------------------------------
// Registry Tests
//
// Author: Alex Freidah
//
// The acceptance test is the first one: a configuration file naming one
// provider per execution family has to reach admission as inputs, with nothing
// in the path needing a cloud account.
//
// The rest are about the decisions a registry makes when something is wrong. A
// provider that fails a refresh must survive it, a disabled provider must
// survive being disabled, and both must still be visible to a plan that has to
// explain why they were not used.
// -------------------------------------------------------------------------------

package registry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// fixturePath is the deployment registering all three families.
var fixturePath = filepath.Join("testdata", "providers.hcl")

// build is the common shape: decode a snippet and construct a registry, failing
// on anything either step reports.
func build(t *testing.T, src string) *Registry {
	t.Helper()

	cfg, diags := config.Load("test.hcl", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("loading configuration failed: %s", diags.Error())
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("building registry failed: %s", err)
	}

	return r
}

// -------------------------------------------------------------------------
// ACCEPTANCE
// -------------------------------------------------------------------------

func TestFixtureLoadsIntoInputs(t *testing.T) {
	t.Parallel()

	cfg, diags := config.LoadFile(fixturePath)
	if diags.HasErrors() {
		t.Fatalf("loading %s failed: %s", fixturePath, diags.Error())
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("building registry failed: %s", err)
	}

	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh failed: %s", err)
	}

	inputs := r.Inputs()
	if len(inputs) != 3 {
		t.Fatalf("got %d inputs, want 3", len(inputs))
	}

	// Ordered by name, so a plan built from them is diffable.
	want := []string{"container-primary", "function-primary", "worker-primary"}
	if diff := cmp.Diff(want, r.Names()); diff != "" {
		t.Errorf("names mismatch (-want +got):\n%s", diff)
	}

	container := inputs[0]

	if !container.Enabled || !container.Healthy {
		t.Errorf("container-primary: enabled=%t healthy=%t, want both true",
			container.Enabled, container.Healthy)
	}

	if diff := cmp.Diff([]job.DriverName{job.DriverContainer}, container.Capabilities.Drivers); diff != "" {
		t.Errorf("drivers mismatch (-want +got):\n%s", diff)
	}

	if got := container.Quota.FreePercent; got != 80 {
		t.Errorf("free percent = %d, want 80", got)
	}

	// The operator's tags have to arrive where a constraint can see them.
	attrs := container.Attributes()
	if got := attrs[plugin.MetaPrefix+"region"]; got != "us-south" {
		t.Errorf("provider.meta.region = %q, want us-south", got)
	}

	// A disabled provider is kept rather than dropped, because a plan that
	// omits it cannot say why it was not considered.
	worker := inputs[2]
	if worker.Enabled {
		t.Error("worker-primary is enabled, want it off")
	}

	// Refresh skips it, so it advertises nothing at all.
	if worker.Capabilities.Drivers != nil {
		t.Errorf("a disabled provider advertised %v", worker.Capabilities.Drivers)
	}
}

// The example an operator is pointed at has to work. One that does not is worse
// than no example, and nothing else checks it.
func TestExampleConfigBuilds(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "examples", "config.hcl")

	cfg, diags := config.LoadFile(path)
	if diags.HasErrors() {
		t.Fatalf("example configuration does not load: %s", diags.Error())
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("example configuration does not build: %s", err)
	}

	if r.Len() == 0 {
		t.Error("example configuration registered no providers")
	}
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

func TestNewNilConfig(t *testing.T) {
	t.Parallel()

	r, err := New(nil)
	if err != nil {
		t.Fatalf("building an empty registry failed: %s", err)
	}

	if r.Len() != 0 {
		t.Errorf("Len() = %d, want 0", r.Len())
	}

	if got := r.Inputs(); len(got) != 0 {
		t.Errorf("Inputs() = %v, want empty", got)
	}
}

func TestNewUnknownType(t *testing.T) {
	t.Parallel()

	cfg, diags := config.Load("test.hcl", []byte(`
provider "mystery" { type = "fake-quantum" }
`))
	if diags.HasErrors() {
		t.Fatalf("loading configuration failed: %s", diags.Error())
	}

	_, err := New(cfg)
	if !errors.Is(err, ErrUnknownProviderType) {
		t.Fatalf("err = %v, want ErrUnknownProviderType", err)
	}

	// The message has to name what was asked for and what exists, because the
	// operator reading it is looking at a typo.
	for _, want := range []string{"fake-quantum", "mystery", TypeFakeContainer} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestNewRejectsBadTags(t *testing.T) {
	t.Parallel()

	cfg, diags := config.Load("test.hcl", []byte(`
provider "tagged" {
  type = "fake-container"

  meta {
    replicas = 3
  }
}
`))
	if diags.HasErrors() {
		t.Fatalf("loading configuration failed: %s", diags.Error())
	}

	if _, err := New(cfg); err == nil {
		t.Fatal("expected a non-string tag to fail construction")
	}
}

func TestNewSortsByName(t *testing.T) {
	t.Parallel()

	r := build(t, `
provider "zulu"   { type = "fake-container" }
provider "alpha"  { type = "fake-function" }
provider "mike"   { type = "fake-worker" }
`)

	want := []string{"alpha", "mike", "zulu"}
	if diff := cmp.Diff(want, r.Names()); diff != "" {
		t.Errorf("names mismatch (-want +got):\n%s", diff)
	}
}

// -------------------------------------------------------------------------
// QUOTA
// -------------------------------------------------------------------------

func TestConfiguredQuota(t *testing.T) {
	t.Parallel()

	r := build(t, `
provider "stated" {
  type = "fake-container"
  quota { free_percent = 60 }
}

provider "spent" {
  type = "fake-container"
  quota { exhausted = true }
}

provider "silent" { type = "fake-container" }
`)

	inputs := r.Inputs()
	byName := make(map[string]int, len(inputs))

	for i, in := range inputs {
		byName[in.Provider] = i
	}

	stated := inputs[byName["stated"]].Quota
	if stated.FreePercent != 60 || !stated.HasHeadroom() {
		t.Errorf("stated quota = %+v, want 60 percent with headroom", stated)
	}

	if spent := inputs[byName["spent"]].Quota; spent.HasHeadroom() {
		t.Error("an exhausted provider reported headroom")
	}

	// An operator who wrote a provider block and no quota block said nothing
	// about consumption, which is an observation that nothing is spent rather
	// than a provider admission has to refuse.
	silent := inputs[byName["silent"]].Quota
	if !silent.HasHeadroom() {
		t.Error("a provider with no quota block has no headroom")
	}

	if silent.ObservedAt.IsZero() {
		t.Error("configured quota was left unobserved")
	}
}

// -------------------------------------------------------------------------
// REFRESH
// -------------------------------------------------------------------------

// failingProvider answers nothing, which is how an unreachable provider looks
// from here.
type failingProvider struct {
	plugin.StatusNotSupported
	plugin.ResultNotSupported
	plugin.CancelNotSupported

	name string
	err  error
}

func (p *failingProvider) Name() string { return p.name }

func (p *failingProvider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.Capabilities{}, p.err
}

func (p *failingProvider) Submit(
	context.Context, execution.ID, *job.Task,
) (plugin.Submission, error) {
	return plugin.Submission{}, p.err
}

func TestRefreshKeepsFailingProvider(t *testing.T) {
	t.Parallel()

	r := build(t, `
provider "flaky"  { type = "fake-container" }
provider "steady" { type = "fake-function" }
`)

	// A first refresh everything answers, so there is a snapshot to lose.
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh failed: %s", err)
	}

	boom := errors.New("connection refused")
	r.entries[0].provider = &failingProvider{name: "flaky", err: boom}

	err := r.Refresh(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the provider failure", err)
	}

	inputs := r.Inputs()

	if inputs[0].Healthy {
		t.Error("flaky is healthy after failing a refresh")
	}

	// The provider survives, and so does what was last known about it: a
	// transient outage must not become a job that cannot be placed later.
	if inputs[0].Capabilities.Drivers == nil {
		t.Error("flaky lost its last known capabilities")
	}

	// A refresh that stopped at the first failure would leave the rest stale.
	if !inputs[1].Healthy || inputs[1].Capabilities.Drivers == nil {
		t.Error("steady was not refreshed after an earlier provider failed")
	}
}

func TestRefreshSkipsDisabled(t *testing.T) {
	t.Parallel()

	r := build(t, `
provider "off" {
  type    = "fake-container"
  enabled = false
}
`)

	boom := errors.New("connection refused")
	r.entries[0].provider = &failingProvider{name: "off", err: boom}

	// A provider nobody wants used is not one worth failing a refresh over.
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh failed on a disabled provider: %s", err)
	}
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

func TestProviderLookup(t *testing.T) {
	t.Parallel()

	r := build(t, `provider "container-primary" { type = "fake-container" }`)

	p, ok := r.Provider("container-primary")
	if !ok {
		t.Fatal("configured provider was not found")
	}

	if got := p.Name(); got != "container-primary" {
		t.Errorf("Name() = %q, want container-primary", got)
	}

	if _, ok := r.Provider("nope"); ok {
		t.Error("an unconfigured name was found")
	}
}

func TestInputsAreIndependent(t *testing.T) {
	t.Parallel()

	r := build(t, `provider "container-primary" { type = "fake-container" }`)

	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh failed: %s", err)
	}

	// Admission runs across every candidate against one cached snapshot, so a
	// caller that sorted the drivers it was handed must not change what the
	// next one sees.
	first := r.Inputs()[0]
	first.Capabilities.Drivers[0] = job.DriverWorker

	if got := r.Inputs()[0].Capabilities.Drivers[0]; got != job.DriverContainer {
		t.Errorf("mutating one input changed the registry: driver = %q", got)
	}
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

func TestBuild(t *testing.T) {
	t.Parallel()

	for _, providerType := range Types() {
		t.Run(providerType, func(t *testing.T) {
			t.Parallel()

			p, err := Build(providerType, "named")
			if err != nil {
				t.Fatalf("building %s failed: %s", providerType, err)
			}

			if got := p.Name(); got != "named" {
				t.Errorf("Name() = %q, want named", got)
			}
		})
	}
}

func TestTypesIsACopy(t *testing.T) {
	t.Parallel()

	got := Types()
	got[0] = "clobbered"

	if Types()[0] != TypeFakeContainer {
		t.Error("mutating the returned slice changed the vocabulary")
	}
}
