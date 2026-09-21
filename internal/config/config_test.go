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

  quota {
    free_percent = 40
    exhausted    = false
  }
}

provider "lambda" {
  type = "fake-function"
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

	if diff := cmp.Diff(ptr.Of(40), ibm.Quota.FreePercent); diff != "" {
		t.Errorf("free_percent mismatch (-want +got):\n%s", diff)
	}

	if file.Providers[1].Quota != nil {
		t.Error("a provider with no quota block decoded one anyway")
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
		"quota above one hundred": {
			src: `
provider "ibm" {
  type = "fake-container"
  quota { free_percent = 140 }
}
`,
			want: "Invalid free quota",
		},
		"quota below zero": {
			src: `
provider "ibm" {
  type = "fake-container"
  quota { free_percent = -1 }
}
`,
			want: "Invalid free quota",
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

func TestValidateAcceptsQuotaBounds(t *testing.T) {
	t.Parallel()

	// Nothing here is arbitrary: zero is an exhausted provider and a hundred is
	// an untouched one, and both are states the ledger will report.
	for _, percent := range []string{"0", "100"} {
		t.Run(percent, func(t *testing.T) {
			t.Parallel()

			load(t, `
provider "ibm" {
  type = "fake-container"
  quota { free_percent = `+percent+` }
}
`)
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
