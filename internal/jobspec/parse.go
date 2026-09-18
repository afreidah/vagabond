// -------------------------------------------------------------------------------
// Parsing
//
// Author: Alex Freidah
//
// Reads a .vagabond.hcl file into a job.File. Decoding runs through gohcl,
// which the specification types are already tagged for, and evaluation happens
// during decode against a context this package builds.
//
// The only schema gohcl cannot describe is a task's config block, whose shape
// depends on the driver. That stays an undecoded body, and this package checks
// what it can about it without pretending to know what belongs inside.
// -------------------------------------------------------------------------------

package jobspec

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Config is everything parsing needs that is not the file itself.
//
// Meta carries the values a caller supplied, which job references interpolate.
// An absent key is not an error here: a job definition is valid without the
// values a particular submission would provide, and deciding otherwise is the
// caller's business rather than the parser's.
type Config struct {
	Filename string
	Source   []byte
	Meta     map[string]string
}

// -------------------------------------------------------------------------
// PARSING
// -------------------------------------------------------------------------

// ParseFile reads and parses the named file.
//
// A read failure is reported as a diagnostic rather than an error so that
// callers have one kind of failure to render, whether the file was unreadable
// or merely wrong.
func ParseFile(path string, meta map[string]string) (*job.File, hcl.Diagnostics) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Cannot read job file",
			Detail:   fmt.Sprintf("Reading %s: %s.", path, err),
		}}
	}

	return Parse(Config{Filename: path, Source: src, Meta: meta})
}

// Parse decodes source into a job file.
//
// Returns whatever it managed to decode alongside its diagnostics, because a
// file with one bad attribute is still worth reporting the rest of.
func Parse(cfg Config) (*job.File, hcl.Diagnostics) {
	body, diags := parseSource(cfg)
	if body == nil {
		return nil, diags
	}

	ctx := EvalContext(cfg.Meta)

	var file job.File

	diags = append(diags, gohcl.DecodeBody(body, ctx, &file)...)
	diags = append(diags, checkConfigBlocks(&file)...)

	return &file, diags
}

// parseSource turns bytes into a body, choosing the parser by file extension.
func parseSource(cfg Config) (hcl.Body, hcl.Diagnostics) {
	name := cfg.Filename
	if name == "" {
		name = "<input>"
	}

	f, diags := hclsyntax.ParseConfig(cfg.Source, name, hcl.InitialPos)
	if f == nil {
		return nil, diags
	}

	return f.Body, diags
}

// -------------------------------------------------------------------------
// CONFIG BLOCKS
// -------------------------------------------------------------------------

// checkConfigBlocks rejects nested blocks inside a task's config.
//
// Driver configs are flat today, and every consumer reads them with
// JustAttributes, which fails on a body containing blocks. Left alone, a nested
// block would surface as a confusing decode failure inside a provider plugin
// long after submission. Refusing it here says so at the line it was written.
//
// Nomad solves the general case in jobspec2/hclutil.BlocksAsAttrs, rewriting a
// body so nested blocks read as object attributes. That belongs here too, the
// day a driver's configuration genuinely nests.
func checkConfigBlocks(file *job.File) hcl.Diagnostics {
	var diags hcl.Diagnostics

	for i := range file.Jobs {
		for j := range file.Jobs[i].Tasks {
			task := &file.Jobs[i].Tasks[j]
			diags = append(diags, checkTaskConfig(task)...)
		}
	}

	return diags
}

// checkTaskConfig reports any block nested inside one task's config.
func checkTaskConfig(task *job.Task) hcl.Diagnostics {
	if task.Config == nil || task.Config.Body == nil {
		return nil
	}

	body, ok := task.Config.Body.(*hclsyntax.Body)
	if !ok {
		return nil
	}

	var diags hcl.Diagnostics

	for _, block := range body.Blocks {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Blocks are not supported in config",
			Detail: fmt.Sprintf(
				"A task config block holds attributes only, and %q is a block. "+
					"Write it as an attribute instead, for example %s = { ... }.",
				block.Type, block.Type),
			Subject: block.DefRange().Ptr(),
		})
	}

	return diags
}
