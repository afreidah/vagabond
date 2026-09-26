// -------------------------------------------------------------------------------
// Task Environment Tests
//
// Author: Alex Freidah
// -------------------------------------------------------------------------------

package job

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

func envBlock(t *testing.T, src string) *RawBlock {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "env.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing: %s", diags.Error())
	}

	return &RawBlock{Body: f.Body}
}

// Keys become valid variable names, and what the author wrote wins over a
// metadata variable of the same name.
func TestEnvironment(t *testing.T) {
	task := &Task{
		Env: envBlock(t, `VAGABOND_META_COMMIT = "pinned"`),
		Meta: map[string]string{
			"commit":     "abc123",
			"build-type": "release",
		},
	}

	env, diags := task.Environment()
	if diags.HasErrors() {
		t.Fatalf("Environment() = %s", diags.Error())
	}

	if env["VAGABOND_META_COMMIT"] != "pinned" {
		t.Errorf("VAGABOND_META_COMMIT = %q, want the author's value", env["VAGABOND_META_COMMIT"])
	}

	if env["VAGABOND_META_BUILD_TYPE"] != "release" {
		t.Errorf("env = %v, want VAGABOND_META_BUILD_TYPE", env)
	}
}

func TestEnvironment_NoEnvBlock(t *testing.T) {
	env, diags := (&Task{Meta: map[string]string{"v": "1"}}).Environment()
	if diags.HasErrors() || env["VAGABOND_META_V"] != "1" {
		t.Errorf("Environment() = %v, %s", env, diags.Error())
	}
}
