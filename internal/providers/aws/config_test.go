// -------------------------------------------------------------------------------
// Lambda Configuration and Credential Tests
//
// Author: Alex Freidah
// -------------------------------------------------------------------------------

package aws

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// body parses an HCL fragment the way the config loader leaves a block.
func body(t *testing.T, src string) hcl.Body {
	t.Helper()

	f, diags := hclsyntax.ParseConfig([]byte(src), "vagabond.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parsing failed: %s", diags.Error())
	}

	return f.Body
}

func TestDecodeConfig(t *testing.T) {
	t.Parallel()

	cfg, diags := decodeConfig("lambda", body(t, `region = "us-east-1"`))
	if diags.HasErrors() {
		t.Fatalf("decode failed: %s", diags.Error())
	}

	if cfg.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1", cfg.Region)
	}
}

func TestDecodeConfig_Refusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body hcl.Body
		want string
	}{
		{name: "no block", body: nil, want: "Missing provider configuration"},
		{name: "empty region", body: body(t, `region = " "`), want: "Empty provider setting"},
		{name: "no region", body: body(t, ``), want: "Missing required argument"},
		{name: "typo", body: body(t, "region = \"us-east-1\"\nregoin = \"x\""), want: "Unsupported argument"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, diags := decodeConfig("lambda", tt.body)
			if !diags.HasErrors() || !strings.Contains(diags.Error(), tt.want) {
				t.Errorf("diags = %v, want %q", diags, tt.want)
			}
		})
	}
}

// -------------------------------------------------------------------------
// CREDENTIALS
// -------------------------------------------------------------------------

// What `aws configure export-credentials --format process` prints.
func TestStaticCredential_ReadsProcessFormat(t *testing.T) {
	t.Parallel()

	provider, err := staticCredential([]byte(`{
  "Version": 1,
  "AccessKeyId": "AKIAEXAMPLE",
  "SecretAccessKey": "secret",
  "SessionToken": "token",
  "Expiration": "2026-09-25T00:00:00Z"
}`))
	if err != nil {
		t.Fatalf("staticCredential() = %v", err)
	}

	creds, err := provider.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}

	if creds.AccessKeyID != "AKIAEXAMPLE" || creds.SecretAccessKey != "secret" || creds.SessionToken != "token" {
		t.Errorf("credentials = %+v", creds)
	}
}

func TestStaticCredential_Refusals(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{ //nolint:gosec // malformed fixtures, not credentials
		"not json":       `AKIAEXAMPLE`,
		"wrong version":  `{"Version": 2, "AccessKeyId": "a", "SecretAccessKey": "b"}`,
		"no secret":      `{"Version": 1, "AccessKeyId": "a"}`,
		"no access key":  `{"Version": 1, "SecretAccessKey": "b"}`,
		"empty document": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := staticCredential([]byte(raw)); err == nil {
				t.Error("staticCredential() accepted it")
			}
		})
	}
}
