// -------------------------------------------------------------------------------
// Configuration Tests
//
// Author: Alex Freidah
//
// The decoding tests are ordinary. What the rest are about is the two readings
// of an absent value that would be wrong: a provider block nobody enabled must
// not be silently off, and a tag nobody wrote must not become an empty string a
// constraint can match.
// -------------------------------------------------------------------------------

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/ptr"
	"github.com/afreidah/vagabond/internal/quota"
)

// load is the common shape: decode a snippet and fail on any diagnostic.
func load(t *testing.T, src string) *File {
	t.Helper()

	file, diags := Load("test.hcl", []byte(src))
	if diags.HasErrors() {
		t.Fatalf("loading failed: %s", diags.Error())
	}

	return file
}

// loadErr is the mirror: decode a snippet expected to fail, and hand back what
// the diagnostics said so a test can assert on it.
func loadErr(t *testing.T, src string) string {
	t.Helper()

	_, diags := Load("test.hcl", []byte(src))
	if !diags.HasErrors() {
		t.Fatal("expected loading to fail")
	}

	return diags.Error()
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

func TestLoadDecodesProviders(t *testing.T) {
	t.Parallel()

	file := load(t, `
provider "ibm-primary" {
  type    = "fake-container"
  enabled = false

  meta {
    region = "us-south"
    tier   = "lite"
  }
}

provider "lambda" {
  type = "fake-function"
}

store {
  dsn = "postgres://vagabond@localhost/vagabond"
}
`)

	if len(file.Providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(file.Providers))
	}

	ibm := file.Providers[0]

	if ibm.Name != "ibm-primary" {
		t.Errorf("name = %q, want ibm-primary", ibm.Name)
	}

	if ibm.Type != "fake-container" {
		t.Errorf("type = %q, want fake-container", ibm.Type)
	}

	if diff := cmp.Diff(ptr.Of(false), ibm.Enabled); diff != "" {
		t.Errorf("enabled mismatch (-want +got):\n%s", diff)
	}

	if file.Store == nil || file.Store.DSN != "postgres://vagabond@localhost/vagabond" {
		t.Errorf("Store = %+v, want the declared DSN", file.Store)
	}
}

// No store block is a ledger kept in memory, not an error.
func TestLoadWithoutStore(t *testing.T) {
	t.Parallel()

	if file := load(t, `provider "ibm" { type = "fake-container" }`); file.Store != nil {
		t.Errorf("Store = %+v, want nil", file.Store)
	}
}

func TestLoadRejectsUnknownAttributes(t *testing.T) {
	t.Parallel()

	// A misspelled attribute that decoded silently would be a provider
	// configured differently than its file says.
	got := loadErr(t, `
provider "ibm" {
  type   = "fake-container"
  enabeld = true
}
`)

	if !strings.Contains(got, "enabeld") {
		t.Errorf("diagnostics do not name the unknown attribute: %s", got)
	}
}

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

func TestValidateRejects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		src  string
		want string
	}{
		"duplicate name": {
			src: `
provider "ibm" { type = "fake-container" }
provider "ibm" { type = "fake-function" }
`,
			want: "Duplicate provider",
		},
		"missing type": {
			src:  `provider "ibm" { type = "" }`,
			want: "Missing provider type",
		},
		"empty store dsn": {
			src:  `store { dsn = "" }`,
			want: "Empty store DSN",
		},
		"two stores in one file": {
			src: `
store { dsn = "postgres://one" }
store { dsn = "postgres://two" }
`,
			want: "Duplicate store",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := loadErr(t, tc.src); !strings.Contains(got, tc.want) {
				t.Errorf("diagnostics = %s, want them to mention %q", got, tc.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

func TestIsEnabledDefaultsToTrue(t *testing.T) {
	t.Parallel()

	file := load(t, `
provider "on-by-default" { type = "fake-container" }

provider "off" {
  type    = "fake-container"
  enabled = false
}

provider "on" {
  type    = "fake-container"
  enabled = true
}
`)

	want := map[string]bool{"on-by-default": true, "off": false, "on": true}

	for i := range file.Providers {
		p := &file.Providers[i]

		if got := p.IsEnabled(); got != want[p.Name] {
			t.Errorf("%s: IsEnabled() = %t, want %t", p.Name, got, want[p.Name])
		}
	}
}

func TestTags(t *testing.T) {
	t.Parallel()

	file := load(t, `
provider "tagged" {
  type = "fake-container"

  meta {
    region = "us-south"
    tier   = "lite"
  }
}

provider "untagged" { type = "fake-container" }
`)

	tags, diags := file.Providers[0].Tags()
	if diags.HasErrors() {
		t.Fatalf("reading tags failed: %s", diags.Error())
	}

	want := map[string]string{"region": "us-south", "tier": "lite"}
	if diff := cmp.Diff(want, tags); diff != "" {
		t.Errorf("tags mismatch (-want +got):\n%s", diff)
	}

	// Nil rather than empty, so that a provider nobody tagged contributes no
	// attributes at all rather than a set of empty ones.
	untagged, diags := file.Providers[1].Tags()
	if diags.HasErrors() {
		t.Fatalf("reading absent tags failed: %s", diags.Error())
	}

	if untagged != nil {
		t.Errorf("untagged provider produced %v, want nil", untagged)
	}
}

func TestTagsRejectsNonStrings(t *testing.T) {
	t.Parallel()

	file := load(t, `
provider "tagged" {
  type = "fake-container"

  meta {
    replicas = 3
  }
}
`)

	// Attributes reach a job as strings a constraint compares. A number that
	// decoded here would be one the operator cannot predict the rendering of.
	_, diags := file.Providers[0].Tags()
	if !diags.HasErrors() {
		t.Fatal("expected a non-string tag to be rejected")
	}

	if !strings.Contains(diags.Error(), "replicas") {
		t.Errorf("diagnostics do not name the tag: %s", diags.Error())
	}
}

// -------------------------------------------------------------------------
// FILES
// -------------------------------------------------------------------------

func TestLoadFileMissing(t *testing.T) {
	t.Parallel()

	_, diags := LoadFile("does-not-exist.hcl")
	if !diags.HasErrors() {
		t.Fatal("expected a missing file to be reported")
	}

	if !strings.Contains(diags.Error(), "Cannot read configuration") {
		t.Errorf("unexpected diagnostics: %s", diags.Error())
	}
}

// -------------------------------------------------------------------------
// DIRECTORIES
// -------------------------------------------------------------------------

// A directory loads every .hcl file in it, which is what makes a provider per
// file possible once real credentials are involved and each one wants its own
// review.
func TestLoadPathMergesADirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	write(t, dir, "ibm.hcl", `provider "ibm" { type = "fake-container" }`)
	write(t, dir, "lambda.hcl", `provider "lambda" { type = "fake-function" }`)
	write(t, dir, "notes.txt", `this is not configuration`)

	file, diags := LoadPath(dir)
	if diags.HasErrors() {
		t.Fatalf("loading %s failed: %s", dir, diags.Error())
	}

	// Sorted by filename, so the same directory reads the same way every run.
	want := []string{"ibm", "lambda"}

	got := make([]string, 0, len(file.Providers))
	for i := range file.Providers {
		got = append(got, file.Providers[i].Name)
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("providers mismatch (-want +got):\n%s", diff)
	}
}

// The name is a routing identifier, so it has to identify one provider whether
// the collision is inside one file or across two.
func TestLoadPathRejectsDuplicatesAcrossFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	write(t, dir, "a.hcl", `provider "ibm" { type = "fake-container" }`)
	write(t, dir, "b.hcl", `provider "ibm" { type = "fake-function" }`)

	_, diags := LoadPath(dir)
	if !diags.HasErrors() {
		t.Fatal("a provider declared in two files was accepted")
	}

	if !strings.Contains(diags.Error(), "Duplicate provider") {
		t.Errorf("unexpected diagnostics: %s", diags.Error())
	}
}

// Two stores would leave file order deciding where the ledger is written.
func TestLoadPathRejectsAStoreDeclaredTwice(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	write(t, dir, "a.hcl", `store { dsn = "postgres://one" }`)
	write(t, dir, "b.hcl", `store { dsn = "postgres://two" }`)

	_, diags := LoadPath(dir)
	if !diags.HasErrors() {
		t.Fatal("a store declared in two files was accepted")
	}

	if !strings.Contains(diags.Error(), "Duplicate store") {
		t.Errorf("unexpected diagnostics: %s", diags.Error())
	}
}

// A store beside the providers, in a file of its own, is the layout a
// directory exists for.
func TestLoadPathMergesAStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	write(t, dir, "ibm.hcl", `provider "ibm" { type = "fake-container" }`)
	write(t, dir, "store.hcl", `store { dsn = "postgres://ledger" }`)

	file, diags := LoadPath(dir)
	if diags.HasErrors() {
		t.Fatalf("LoadPath() = %s", diags.Error())
	}

	if file.Store == nil || file.Store.DSN != "postgres://ledger" {
		t.Errorf("Store = %+v, want the one store.hcl declares", file.Store)
	}
}

// A directory with nothing in it is a mistake worth naming, not an empty
// configuration: somebody pointed at the wrong place.
func TestLoadPathRejectsAnEmptyDirectory(t *testing.T) {
	t.Parallel()

	_, diags := LoadPath(t.TempDir())
	if !diags.HasErrors() {
		t.Fatal("an empty directory was accepted")
	}

	if !strings.Contains(diags.Error(), "Empty configuration directory") {
		t.Errorf("unexpected diagnostics: %s", diags.Error())
	}
}

func TestLoadPathReadsAFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write(t, dir, "one.hcl", `provider "ibm" { type = "fake-container" }`)

	file, diags := LoadPath(filepath.Join(dir, "one.hcl"))
	if diags.HasErrors() {
		t.Fatalf("loading failed: %s", diags.Error())
	}

	if len(file.Providers) != 1 {
		t.Errorf("got %d providers, want 1", len(file.Providers))
	}
}

func TestLoadPathMissing(t *testing.T) {
	t.Parallel()

	_, diags := LoadPath(filepath.Join(t.TempDir(), "absent"))
	if !diags.HasErrors() {
		t.Fatal("a missing path was accepted")
	}
}

// write puts one file in a directory.
func write(t *testing.T, dir, name, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %s", name, err)
	}
}

// -------------------------------------------------------------------------
// THE OPAQUE BLOCK
// -------------------------------------------------------------------------

// A provider's own settings are not Vagabond's to understand. Cloud Run needs
// a project and a runtime identity, Code Engine needs a GUID, and declaring
// either here would make every provider carry another's fields.
func TestProviderConfigIsOpaque(t *testing.T) {
	t.Parallel()

	file := load(t, `
provider "gcp-cloud-run" {
  type = "cloud-run"

  config {
    project                 = "munchbox-66afc"
    region                  = "us-central1"
    runtime_service_account = "vagabond-run@munchbox-66afc.iam.gserviceaccount.com"

    nested {
      anything = "goes"
    }
  }
}
`)

	body := file.Providers[0].ConfigBody()
	if body == nil {
		t.Fatal("the config block did not reach the provider")
	}

	// Undecoded, so a plugin can ask for exactly the attributes it knows and
	// report a typo against the line the operator wrote it on. A nested block
	// is fine here precisely because nothing flattened it.
	content, _, diags := body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "project"}},
	})
	if diags.HasErrors() {
		t.Fatalf("reading one attribute failed: %s", diags.Error())
	}

	value, valueDiags := content.Attributes["project"].Expr.Value(nil)
	if valueDiags.HasErrors() {
		t.Fatalf("evaluating project failed: %s", valueDiags.Error())
	}

	if got := value.AsString(); got != "munchbox-66afc" {
		t.Errorf("project = %q, want munchbox-66afc", got)
	}
}

// A provider needing no settings should not have to declare an empty block.
func TestProviderWithoutConfigBlock(t *testing.T) {
	t.Parallel()

	file := load(t, `provider "fake" { type = "fake-container" }`)

	if file.Providers[0].ConfigBody() != nil {
		t.Error("a provider with no config block produced a body")
	}
}

// -------------------------------------------------------------------------
// QUOTA POOLS
// -------------------------------------------------------------------------

func TestLoadDecodesPools(t *testing.T) {
	t.Parallel()

	file := load(t, `
provider "aws-lambda" {
  type = "fake-function"

  pool "requests" {
    meter  = "executions"
    limit  = 1000000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 400000
    period = "monthly"
  }
}
`)

	want := []quota.PoolSpec{
		{Name: "requests", Meter: quota.MeterExecutions, Limit: 1_000_000, Period: quota.PeriodMonthly},
		{Name: "compute", Meter: quota.MeterGBSeconds, Limit: 400_000, Period: quota.PeriodMonthly},
	}

	if diff := cmp.Diff(want, file.Providers[0].PoolSpecs()); diff != "" {
		t.Errorf("PoolSpecs() mismatch (-want +got):\n%s", diff)
	}
}

// No pools is unlimited, not refused, so it must decode to nothing rather than
// to a pool that cannot be satisfied.
func TestProviderWithoutPools(t *testing.T) {
	t.Parallel()

	file := load(t, `provider "fake" { type = "fake-container" }`)

	if specs := file.Providers[0].PoolSpecs(); specs != nil {
		t.Errorf("PoolSpecs() = %v, want nil", specs)
	}
}

func TestPoolValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "unknown meter",
			src: `provider "p" {
  type = "fake-function"

  pool "cpu" {
    meter  = "vcpu_seconds"
    limit  = 100
    period = "monthly"
  }
}`,
			want: "not something Vagabond counts",
		},
		{
			name: "unknown period",
			src: `provider "p" {
  type = "fake-function"

  pool "requests" {
    meter  = "executions"
    limit  = 100
    period = "weekly"
  }
}`,
			want: `resets "weekly"`,
		},
		{
			name: "zero limit",
			src: `provider "p" {
  type = "fake-function"

  pool "requests" {
    meter  = "executions"
    limit  = 0
    period = "monthly"
  }
}`,
			want: "must be positive",
		},
		{
			name: "negative limit",
			src: `provider "p" {
  type = "fake-function"

  pool "requests" {
    meter  = "executions"
    limit  = -5
    period = "monthly"
  }
}`,
			want: "must be positive",
		},
		{
			name: "duplicate pool name",
			src: `provider "p" {
  type = "fake-function"

  pool "requests" {
    meter  = "executions"
    limit  = 100
    period = "monthly"
  }

  pool "requests" {
    meter  = "executions"
    limit  = 200
    period = "daily"
  }
}`,
			want: "declared twice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := loadErr(t, tt.src); !strings.Contains(got, tt.want) {
				t.Errorf("diagnostics = %q, want them to mention %q", got, tt.want)
			}
		})
	}
}

// Every attribute is required, so an operator cannot leave the period to a
// default that silently enforces a daily budget monthly.
func TestPoolRequiresEveryAttribute(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"no meter": `
    limit  = 100
    period = "monthly"`,
		"no limit": `
    meter  = "executions"
    period = "monthly"`,
		"no period": `
    meter = "executions"
    limit = 100`,
	}

	for name, attrs := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			src := `provider "p" {
  type = "fake-function"

  pool "requests" {` + attrs + `
  }
}`

			if got := loadErr(t, src); !strings.Contains(got, "Missing required argument") {
				t.Errorf("diagnostics = %q, want a missing argument error", got)
			}
		})
	}
}

// The diagnostic lists what the operator could have written, which is the whole
// value of transcribing a pricing page by hand.
func TestPoolDiagnosticListsVocabulary(t *testing.T) {
	t.Parallel()

	got := loadErr(t, `provider "p" {
  type = "fake-function"

  pool "cpu" {
    meter  = "vcpu_seconds"
    limit  = 100
    period = "monthly"
  }
}`)

	for _, m := range quota.Meters() {
		if !strings.Contains(got, string(m)) {
			t.Errorf("diagnostics do not mention the %s meter: %q", m, got)
		}
	}
}
