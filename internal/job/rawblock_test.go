// -------------------------------------------------------------------------------
// Undecoded Block Tests
//
// Author: Alex Freidah
//
// Attributes is the seam between a body and the string map a consumer wants, so
// the cases worth covering are the ones where a value is not a string: the
// point of returning diagnostics rather than a map alone is being able to say
// which key was wrong and where.
// -------------------------------------------------------------------------------

package job

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// rawBlock builds a block from a snippet.
func rawBlock(t *testing.T, src string) *RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "test.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing: %s", diags.Error())
	}

	return &RawBlock{Body: f.Body}
}

func TestRawBlock_Attributes(t *testing.T) {
	block := rawBlock(t, `
CI               = "true"
TF_IN_AUTOMATION = "true"
`)

	attrs, diags := block.Attributes(nil)
	if diags.HasErrors() {
		t.Fatalf("Attributes returned diagnostics: %s", diags.Error())
	}

	if attrs["CI"] != "true" || attrs["TF_IN_AUTOMATION"] != "true" {
		t.Errorf("attributes = %v, want both set to true", attrs)
	}
}

// Numbers and booleans convert, because a job author writing memory = 512 in an
// env block means the string, and refusing it would be pedantry.
func TestRawBlock_AttributesConvertScalars(t *testing.T) {
	block := rawBlock(t, `
count   = 3
enabled = true
`)

	attrs, diags := block.Attributes(nil)
	if diags.HasErrors() {
		t.Fatalf("Attributes returned diagnostics: %s", diags.Error())
	}

	if attrs["count"] != "3" || attrs["enabled"] != "true" {
		t.Errorf("attributes = %v, want count 3 and enabled true", attrs)
	}
}

// A list cannot be an environment variable, and saying which key was wrong is
// the whole reason this returns diagnostics rather than a map alone.
func TestRawBlock_AttributesRejectsNonScalar(t *testing.T) {
	block := rawBlock(t, `args = ["-lc", "echo hello"]`)

	_, diags := block.Attributes(nil)
	if !diags.HasErrors() {
		t.Fatal("a list value produced no diagnostics")
	}

	if !strings.Contains(diags.Error(), "args") {
		t.Errorf("diagnostics do not name the offending key:\n%s", diags.Error())
	}

	if diags[0].Subject == nil {
		t.Error("the diagnostic carries no source range")
	}
}

// An unresolved reference is skipped rather than reported. Validation runs
// before values are supplied, and a job that interpolates metadata is not
// wrong for doing so.
func TestRawBlock_AttributesSkipsUnknownValues(t *testing.T) {
	block := rawBlock(t, `ref = meta.version`)

	ctx := &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"meta": cty.ObjectVal(map[string]cty.Value{
				"version": cty.UnknownVal(cty.String),
			}),
		},
	}

	attrs, diags := block.Attributes(ctx)
	if diags.HasErrors() {
		t.Fatalf("Attributes returned diagnostics: %s", diags.Error())
	}

	if _, ok := attrs["ref"]; ok {
		t.Errorf("an unknown value was recorded as %q", attrs["ref"])
	}
}

// A null is skipped for the same reason: absent and empty are different, and
// recording an empty string would erase that.
func TestRawBlock_AttributesSkipsNull(t *testing.T) {
	block := rawBlock(t, `ref = null`)

	attrs, diags := block.Attributes(nil)
	if diags.HasErrors() {
		t.Fatalf("Attributes returned diagnostics: %s", diags.Error())
	}

	if _, ok := attrs["ref"]; ok {
		t.Error("a null value was recorded")
	}
}

// -------------------------------------------------------------------------
// IMAGE
// -------------------------------------------------------------------------

// The config block's shape belongs to the driver, with one exception: whether a
// task names an image is something admission has to know, because a provider
// can run containers without running anyone's container.
func TestTask_Image(t *testing.T) {
	tests := map[string]struct {
		config *RawBlock
		want   string
	}{
		"names one": {
			config: rawBlock(t, `
image   = "golang:1.27"
command = "go test ./..."
`),
			want: "golang:1.27",
		},
		"names something else": {
			config: rawBlock(t, `handler = "main.handler"`),
			want:   "",
		},
		"no config at all": {
			config: nil,
			want:   "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			task := &Task{Name: "test", Config: tc.config}

			got, diags := task.Image(nil)
			if diags.HasErrors() {
				t.Fatalf("Image returned diagnostics: %s", diags.Error())
			}

			if got != tc.want {
				t.Errorf("Image() = %q, want %q", got, tc.want)
			}
		})
	}
}
