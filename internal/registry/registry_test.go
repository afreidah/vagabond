// -------------------------------------------------------------------------------
// Registry Tests
//
// Author: Alex Freidah
//
// The acceptance test is first: a configuration naming one provider per
// execution family reaches admission as inputs, with no cloud account in the
// path. The rest cover what a registry does when something is wrong: a provider
// that fails a refresh or is disabled survives, and stays visible to a plan.
// -------------------------------------------------------------------------------

package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/config"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/providers/gcp"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

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

	r, diags := New(t.Context(), cfg)
	if diags.HasErrors() {
		t.Fatalf("building registry failed: %s", diags.Error())
	}

	return r
}

// -------------------------------------------------------------------------
// ACCEPTANCE
// -------------------------------------------------------------------------

// The fixture deployment reaches admission as inputs, ordered, tagged, and
// with its disabled provider kept.
func TestFixtureLoadsIntoInputs(t *testing.T) {
	t.Parallel()

	cfg, diags := config.LoadFile(fixturePath)
	if diags.HasErrors() {
		t.Fatalf("loading %s failed: %s", fixturePath, diags.Error())
	}

	r, diags := New(t.Context(), cfg)
	if diags.HasErrors() {
		t.Fatalf("building registry failed: %s", diags.Error())
	}

	if err := r.Refresh(t.Context()); err != nil {
		t.Fatalf("refresh failed: %s", err)
	}

	inputs := r.Inputs(job.DefaultNamespace, nil)
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

	if !container.Limits.Unlimited() {
		t.Error("container-primary declared no pools but enforces some")
	}

	if inputs[1].Limits.Unlimited() {
		t.Error("function-primary declared a pool that did not reach admission")
	}

	// The operator's tags have to arrive where a constraint can see them.
	attrs := container.Attributes(quota.Execution{})
	if got := attrs[plugin.MetaPrefix+"region"]; got != "us-south" {
		t.Errorf("provider.meta.region = %q, want us-south", got)
	}

	// A disabled provider is kept, so a plan can say why it was not used.
	worker := inputs[2]
	if worker.Enabled {
		t.Error("worker-primary is enabled, want it off")
	}

	// Refresh skips it, so it advertises nothing at all.
	if worker.Capabilities.Drivers != nil {
		t.Errorf("a disabled provider advertised %v", worker.Capabilities.Drivers)
	}
}

// The example configuration an operator is pointed at loads and builds;
// nothing else checks it.
func TestExampleConfigBuilds(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "examples", "config.hcl")

	cfg, diags := config.LoadFile(path)
	if diags.HasErrors() {
		t.Fatalf("example configuration does not load: %s", diags.Error())
	}

	r, diags := New(t.Context(), cfg)
	if diags.HasErrors() {
		t.Fatalf("example configuration does not build: %s", diags.Error())
	}

	if r.Len() == 0 {
		t.Error("example configuration registered no providers")
	}
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// A nil configuration builds an empty registry.
func TestNewNilConfig(t *testing.T) {
	t.Parallel()

	r, diags := New(t.Context(), nil)
	if diags.HasErrors() {
		t.Fatalf("building an empty registry failed: %s", diags.Error())
	}

	if r.Len() != 0 {
		t.Errorf("Len() = %d, want 0", r.Len())
	}

	if got := r.Inputs(job.DefaultNamespace, nil); len(got) != 0 {
		t.Errorf("Inputs() = %v, want empty", got)
	}
}

// An unknown provider type is refused, naming the type, the provider and the
// known types.
func TestNewUnknownType(t *testing.T) {
	t.Parallel()

	cfg, diags := config.Load("test.hcl", []byte(`
provider "mystery" { type = "fake-quantum" }
`))
	if diags.HasErrors() {
		t.Fatalf("loading configuration failed: %s", diags.Error())
	}

	_, diags = New(t.Context(), cfg)
	if !diags.HasErrors() {
		t.Fatal("an unknown provider type was accepted")
	}

	// The operator reading it is looking at a typo.
	for _, want := range []string{"fake-quantum", "mystery", TypeFakeContainer} {
		if !strings.Contains(diags.Error(), want) {
			t.Errorf("diagnostics %q do not mention %q", diags.Error(), want)
		}
	}
}

// A tag that is not a string fails construction.
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

	if _, diags := New(t.Context(), cfg); !diags.HasErrors() {
		t.Fatal("expected a non-string tag to fail construction")
	}
}

// Providers are held in name order, whatever order they were configured in.
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

// Usage comes from whatever the caller reads it through, so plan and run see
// the same ledger. Limits are what the ledger is built over.
func TestInputsCarryLimitsAndUsage(t *testing.T) {
	t.Parallel()

	r := build(t, `
provider "metered" {
  type = "fake-function"

  pool "requests" {
    meter  = "executions"
    limit  = 100
    period = "monthly"
  }
}

provider "unmetered" { type = "fake-container" }
`)

	usage := func(_, provider string) (quota.PoolUsage, quota.PoolUsage) {
		if provider == "metered" {
			return quota.PoolUsage{"requests": 40}, nil
		}

		return nil, nil
	}

	inputs := r.Inputs(job.DefaultNamespace, usage)
	byName := make(map[string]scheduler.Input, len(inputs))

	for _, in := range inputs {
		byName[in.Provider] = in
	}

	metered := byName["metered"]

	if got := metered.Usage["requests"]; got != 40 {
		t.Errorf("metered requests = %d, want 40", got)
	}

	if got := metered.FreePercent(quota.Execution{}); got != 60 {
		t.Errorf("metered FreePercent() = %d, want 60", got)
	}

	if !byName["unmetered"].Limits.Unlimited() {
		t.Error("a provider with no pools enforces some")
	}

	budgets := r.Budgets()
	if len(budgets.Totals) != 2 || budgets.Total("metered").Unlimited() {
		t.Errorf("Budgets().Totals = %v, want both providers with metered's pool", budgets.Totals)
	}
}

// A namespace's share reaches admission beside the provider's total, and a
// namespace with none leaves the total as the only limit.
func TestInputsCarryTheNamespaceShare(t *testing.T) {
	t.Parallel()

	r := build(t, `
provider "fn" {
  type = "fake-function"

  pool "requests" {
    meter  = "executions"
    limit  = 100
    period = "monthly"
  }
}

namespace "ci" {
  quota "fn" {
    pool "requests" {
      meter  = "executions"
      limit  = 10
      period = "monthly"
    }
  }
}

namespace "empty" {}
`)

	usage := func(_, _ string) (quota.PoolUsage, quota.PoolUsage) {
		return quota.PoolUsage{"requests": 5}, quota.PoolUsage{"requests": 9}
	}

	ci := r.Inputs("ci", usage)[0]

	if ci.Share.Unlimited() || ci.ShareUsage["requests"] != 9 {
		t.Errorf("ci input = %+v, want its share and share usage", ci)
	}

	// The share has 1 of 10 left, tighter than the total's 95 of 100.
	if got := ci.FreePercent(quota.Execution{}); got != 10 {
		t.Errorf("FreePercent() = %d, want the share's 10", got)
	}

	if empty := r.Inputs("empty", nil)[0]; !empty.Share.Unlimited() {
		t.Error("a namespace with no quotas has a share")
	}

	for _, name := range []string{"ci", "empty", job.DefaultNamespace} {
		if !r.HasNamespace(name) {
			t.Errorf("HasNamespace(%q) = false", name)
		}
	}

	if r.HasNamespace("undeclared") {
		t.Error("HasNamespace() accepted an undeclared namespace")
	}
}

// A nil reader is a ledger with nothing charged, which is what plan reads
// before any store exists.
func TestInputsWithoutUsage(t *testing.T) {
	t.Parallel()

	r := build(t, `provider "p" { type = "fake-container" }`)

	if got := r.Inputs(job.DefaultNamespace, nil)[0].Usage; got != nil {
		t.Errorf("Usage = %v, want nil", got)
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

// Name returns the name the provider was given.
func (p *failingProvider) Name() string { return p.name }

// Capabilities fails with err.
func (p *failingProvider) Capabilities(context.Context) (plugin.Capabilities, error) {
	return plugin.Capabilities{}, p.err
}

// Submit fails with err.
func (p *failingProvider) Submit(
	context.Context, execution.ID, *job.Task,
) (plugin.Submission, error) {
	return plugin.Submission{}, p.err
}

// A provider that fails a refresh is marked unhealthy but kept with its last
// snapshot, and the providers after it are still refreshed.
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

	inputs := r.Inputs(job.DefaultNamespace, nil)

	if inputs[0].Healthy {
		t.Error("flaky is healthy after failing a refresh")
	}

	// A transient outage must not leave the provider unplaceable later.
	if inputs[0].Capabilities.Drivers == nil {
		t.Error("flaky lost its last known capabilities")
	}

	// A refresh that stopped at the first failure would leave the rest stale.
	if !inputs[1].Healthy || inputs[1].Capabilities.Drivers == nil {
		t.Error("steady was not refreshed after an earlier provider failed")
	}
}

// A disabled provider is not asked, so a failing one fails no refresh.
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

	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh failed on a disabled provider: %s", err)
	}
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// A configured provider is found by name, and an unconfigured name is not.
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

// Every Inputs call hands out its own copy of the cached snapshot, so a caller
// that changes its drivers does not change what the next one sees.
func TestInputsAreIndependent(t *testing.T) {
	t.Parallel()

	r := build(t, `provider "container-primary" { type = "fake-container" }`)

	if err := r.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh failed: %s", err)
	}

	first := r.Inputs(job.DefaultNamespace, nil)[0]
	first.Capabilities.Drivers[0] = job.DriverWorker

	if got := r.Inputs(job.DefaultNamespace, nil)[0].Capabilities.Drivers[0]; got != job.DriverContainer {
		t.Errorf("mutating one input changed the registry: driver = %q", got)
	}
}

// -------------------------------------------------------------------------
// PROVIDER TYPES
// -------------------------------------------------------------------------

// Every fake builds from nothing but a name, so the registry and job plan run
// with no cloud account.
func TestBuild(t *testing.T) {
	t.Parallel()

	for _, providerType := range []string{TypeFakeContainer, TypeFakeFunction, TypeFakeWorker} {
		t.Run(providerType, func(t *testing.T) {
			t.Parallel()

			p, diags := Build(t.Context(), providerType, Settings{Name: "named"})
			if diags.HasErrors() {
				t.Fatalf("building %s failed: %s", providerType, diags.Error())
			}

			if got := p.Name(); got != "named" {
				t.Errorf("Name() = %q, want named", got)
			}
		})
	}
}

// A real provider built with no configuration names the missing config block,
// rather than each absent attribute.
func TestBuildRealProviderNeedsConfig(t *testing.T) {
	t.Parallel()

	_, diags := Build(t.Context(), gcp.Type, Settings{Name: "gcp-cloud-run"})
	if !diags.HasErrors() {
		t.Fatal("a cloud-run provider built with no configuration")
	}

	if !strings.Contains(diags.Error(), "config block") {
		t.Errorf("diagnostics do not name the missing block: %s", diags.Error())
	}
}

// Changing the slice Types returns does not change the vocabulary.
func TestTypesIsACopy(t *testing.T) {
	t.Parallel()

	first := Types()[0]

	got := Types()
	got[0] = "clobbered"

	if Types()[0] != first {
		t.Error("mutating the returned slice changed the vocabulary")
	}
}

// -------------------------------------------------------------------------
// CREDENTIALS
// -------------------------------------------------------------------------

// A resolved credential reaches the plugin as bytes, and the plugin never
// learns whether it came from a file, the environment or a command.
func TestNewResolvesCredentials(t *testing.T) {
	t.Setenv("VAGABOND_TEST_KEY", "a-secret")

	cfg, diags := config.Load("test.hcl", []byte(`
provider "needs-a-key" {
  type = "fake-container"
  credentials { env = "VAGABOND_TEST_KEY" }
}
`))
	if diags.HasErrors() {
		t.Fatalf("loading configuration failed: %s", diags.Error())
	}

	if _, diags := New(t.Context(), cfg); diags.HasErrors() {
		t.Fatalf("building with a credential failed: %s", diags.Error())
	}
}

// A credential that cannot be resolved fails its provider at build, naming the
// provider and the source, rather than as a rejection later.
func TestNewReportsCredentialFailure(t *testing.T) {
	t.Parallel()

	cfg, diags := config.Load("test.hcl", []byte(`
provider "unreachable-secret" {
  type = "fake-container"
  credentials { file = "/nowhere/key.json" }
}
`))
	if diags.HasErrors() {
		t.Fatalf("loading configuration failed: %s", diags.Error())
	}

	_, diags = New(t.Context(), cfg)
	if !diags.HasErrors() {
		t.Fatal("an unresolvable credential was accepted")
	}

	for _, want := range []string{"unreachable-secret", "/nowhere/key.json"} {
		if !strings.Contains(diags.Error(), want) {
			t.Errorf("diagnostics do not mention %q: %s", want, diags.Error())
		}
	}
}

// One broken provider does not hide the next one's problem.
func TestNewCollectsEveryProblem(t *testing.T) {
	t.Parallel()

	cfg, diags := config.Load("test.hcl", []byte(`
provider "bad-type"   { type = "fake-quantum" }
provider "bad-secret" {
  type = "fake-container"
  credentials { file = "/nowhere/key.json" }
}
`))
	if diags.HasErrors() {
		t.Fatalf("loading configuration failed: %s", diags.Error())
	}

	_, diags = New(t.Context(), cfg)

	// Diagnostics.Error renders only the first, so read every one.
	var reported string
	for _, d := range diags {
		reported += d.Summary + " " + d.Detail + "\n"
	}

	for _, want := range []string{"bad-type", "bad-secret"} {
		if !strings.Contains(reported, want) {
			t.Errorf("diagnostics stopped before %q:\n%s", want, reported)
		}
	}
}

// -------------------------------------------------------------------------
// RECREDENTIAL
// -------------------------------------------------------------------------

// recredentialProvider records every credential it is handed, or refuses them
// all with err.
type recredentialProvider struct {
	failingProvider

	handed [][]byte
	err    error
}

// Recredential records credential, or refuses it.
func (p *recredentialProvider) Recredential(_ context.Context, credential []byte) error {
	if p.err != nil {
		return p.err
	}

	p.handed = append(p.handed, credential)

	return nil
}

// withKeyFile builds a registry of one provider reading its credential from a
// file holding "old", swaps in p, and returns the file's path.
func withKeyFile(t *testing.T, p *recredentialProvider) (*Registry, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "key")
	writeKey(t, path, "old")

	r := build(t, `
provider "cloud" {
  type = "fake-container"
  credentials { file = "`+path+`" }
}
`)
	p.name = "cloud"
	r.entries[0].provider = p

	return r, path
}

// writeKey replaces the key file's contents.
func writeKey(t *testing.T, path, key string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}
}

// A rejected credential is read again from its source and handed over. A
// second rejection within recredentialEvery retries on that one rather than
// reading the source again.
func TestRecredential_ResolvesAgainAndHandsOver(t *testing.T) {
	t.Parallel()

	p := &recredentialProvider{}
	r, path := withKeyFile(t, p)

	writeKey(t, path, "new")

	if retry, err := r.Recredential(t.Context(), "cloud"); !retry || err != nil {
		t.Fatalf("Recredential() = %t, %v; want a retry", retry, err)
	}

	writeKey(t, path, "newer")

	if retry, err := r.Recredential(t.Context(), "cloud"); !retry || err != nil {
		t.Fatalf("second Recredential() = %t, %v; want a retry", retry, err)
	}

	if diff := cmp.Diff([][]byte{[]byte("new")}, p.handed); diff != "" {
		t.Errorf("credentials handed over (-want +got):\n%s", diff)
	}
}

// With nothing to refresh, a caller is told not to retry, and nothing fails.
func TestRecredential_NothingToRefresh(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		src  string
		name string
	}{
		"unknown provider": {
			src:  `provider "cloud" { type = "fake-container" }`,
			name: "elsewhere",
		},
		"no credentials block": {
			src:  `provider "cloud" { type = "fake-container" }`,
			name: "cloud",
		},
		"cannot take a credential": {
			src: `
provider "cloud" {
  type = "fake-container"
  credentials { env = "PATH" }
}
`,
			name: "cloud",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			retry, err := build(t, tc.src).Recredential(t.Context(), tc.name)
			if retry || err != nil {
				t.Errorf("Recredential() = %t, %v; want no retry and no error", retry, err)
			}
		})
	}
}

// A source that can no longer be read fails the refresh, naming the provider.
func TestRecredential_SourceUnreadable(t *testing.T) {
	t.Parallel()

	p := &recredentialProvider{}
	r, path := withKeyFile(t, p)

	if err := os.Remove(path); err != nil {
		t.Fatalf("removing the key: %v", err)
	}

	retry, err := r.Recredential(t.Context(), "cloud")
	if retry || err == nil || !strings.Contains(err.Error(), "cloud") {
		t.Errorf("Recredential() = %t, %v; want no retry and an error naming the provider", retry, err)
	}

	if len(p.handed) != 0 {
		t.Errorf("handed over %d credentials, want none", len(p.handed))
	}
}

// A provider that refuses the new credential fails the refresh with its
// error.
func TestRecredential_ProviderRefuses(t *testing.T) {
	t.Parallel()

	unusable := errors.New("unusable key")
	r, _ := withKeyFile(t, &recredentialProvider{err: unusable})

	retry, err := r.Recredential(t.Context(), "cloud")
	if retry || !errors.Is(err, unusable) {
		t.Errorf("Recredential() = %t, %v; want no retry and the provider's error", retry, err)
	}
}
