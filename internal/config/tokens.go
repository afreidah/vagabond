// -------------------------------------------------------------------------------
// API Tokens
//
// Author: Alex Freidah
//
// The tokens the server accepts on its API. Each names where its secret comes
// from, the same credentials block a provider uses, and what it may do: act in
// every namespace as a management token, or read or write in the namespaces it
// lists. Declaring any token turns authentication on.
// -------------------------------------------------------------------------------

package config

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// The policies a token can hold in a namespace. Write includes read.
const (
	PolicyRead  = "read"
	PolicyWrite = "write"
)

// MinTokenBytes is the shortest secret a token may have, so a guessable one
// such as a word stops the server from starting.
const MinTokenBytes = 16

// Token is one API token, labelled with the name logs and records show for the
// requests it makes. It is a management token or lists its namespaces, never
// both.
type Token struct {
	Name        string            `hcl:"name,label"`
	Credentials *CredentialsBlock `hcl:"credentials,block"`
	Management  bool              `hcl:"management,optional"`
	Namespaces  []TokenNamespace  `hcl:"namespace,block"`
}

// TokenNamespace is a namespace a token acts in, labelled with its name, and
// whether it may only read there or change things too.
type TokenNamespace struct {
	Name   string `hcl:"name,label"`
	Policy string `hcl:"policy"`
}

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

// validateTokens reports tokens declared twice and tokens that mean nothing:
// no secret, both or neither of management and namespaces, a policy other
// than read or write, or a namespace that is not declared.
func (f *File) validateTokens() hcl.Diagnostics {
	var diags hcl.Diagnostics

	declared := map[string]bool{job.DefaultNamespace: true}
	for i := range f.Namespaces {
		declared[f.Namespaces[i].Name] = true
	}

	seen := make(map[string]bool, len(f.Tokens))

	for i := range f.Tokens {
		t := &f.Tokens[i]

		if seen[t.Name] {
			diags = append(diags, tokenDiag("Duplicate token", fmt.Sprintf("Two tokens are named %q. "+
				"The name is what logs and records show for a request, so it must identify one.", t.Name)))
		}

		seen[t.Name] = true

		diags = append(diags, t.validate(declared)...)
	}

	return diags
}

// validate reports one token that means nothing, against the namespaces the
// configuration declares.
func (t *Token) validate(declared map[string]bool) hcl.Diagnostics {
	var diags hcl.Diagnostics

	owner := fmt.Sprintf("Token %q", t.Name)

	if t.Credentials == nil {
		diags = append(diags, tokenDiag("Missing token secret", owner+
			" has no credentials block, so there is no secret to compare requests against."))
	}

	diags = append(diags, t.Credentials.validate(owner)...)

	switch {
	case t.Management && len(t.Namespaces) > 0:
		diags = append(diags, tokenDiag("Management token lists namespaces", owner+
			" is a management token, which acts in every namespace, so its namespace blocks mean nothing."))
	case !t.Management && len(t.Namespaces) == 0:
		diags = append(diags, tokenDiag("Token allows nothing", owner+
			" names no namespace and is not a management token. Add a namespace block, or set management = true."))
	}

	named := make(map[string]bool, len(t.Namespaces))

	for _, ns := range t.Namespaces {
		switch {
		case !declared[ns.Name]:
			diags = append(diags, tokenDiag("Unknown namespace in token", fmt.Sprintf("%s names namespace "+
				"%q, which is not declared. Declare it with a namespace block, or name default.", owner, ns.Name)))
		case named[ns.Name]:
			diags = append(diags, tokenDiag("Duplicate token namespace",
				fmt.Sprintf("%s names namespace %q twice.", owner, ns.Name)))
		}

		named[ns.Name] = true

		if ns.Policy != PolicyRead && ns.Policy != PolicyWrite {
			diags = append(diags, tokenDiag("Unknown token policy", fmt.Sprintf("%s gives namespace %q "+
				"the policy %q. Valid policies are read and write.", owner, ns.Name, ns.Policy)))
		}
	}

	return diags
}

// tokenDiag is an error about a token block.
func tokenDiag(summary, detail string) *hcl.Diagnostic {
	return &hcl.Diagnostic{Severity: hcl.DiagError, Summary: summary, Detail: detail}
}

// -------------------------------------------------------------------------
// SECRET
// -------------------------------------------------------------------------

// Secret reads the token's secret with surrounding whitespace removed, so a
// file written with echo works, and refuses one shorter than MinTokenBytes.
func (t *Token) Secret(ctx context.Context) (string, error) {
	raw, err := t.Credentials.Resolve(ctx)
	if err != nil {
		return "", fmt.Errorf("token %q: %w", t.Name, err)
	}

	secret := strings.TrimSpace(string(raw))
	if len(secret) < MinTokenBytes {
		return "", fmt.Errorf("token %q: the secret is %d bytes, and must be at least %d",
			t.Name, len(secret), MinTokenBytes)
	}

	return secret, nil
}
