// -------------------------------------------------------------------------------
// Parsing
//
// Author: Alex Freidah
//
// Reads a .vagabond.hcl file into a job.File. Decoding runs through gohcl, with
// evaluation during decode against a context this package builds. A task's
// config block, whose shape depends on the driver, stays an undecoded body that
// this package checks only for what it can know.
// -------------------------------------------------------------------------------

package jobspec

import (
	"fmt"
	"maps"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Config is everything parsing needs besides the file. Meta carries the values
// a caller supplied; an absent key is the caller's concern, not the parser's.
type Config struct {
	Filename string
	Source   []byte
	Meta     map[string]string
}

// Parsed is a decoded specification with its source, so diagnostics can be
// rendered against the original text.
type Parsed struct {
	Spec     *job.File
	Filename string
	Source   *hcl.File
}

// Files returns the source in the shape hcl's diagnostic writer expects, and is
// safe on nil because a caller rendering diagnostics often had a failed parse.
func (p *Parsed) Files() map[string]*hcl.File {
	if p == nil || p.Source == nil {
		return nil
	}

	return map[string]*hcl.File{p.Filename: p.Source}
}

// -------------------------------------------------------------------------
// PARSING
// -------------------------------------------------------------------------

// ParseFile reads and parses the named file, reporting a read failure as a
// diagnostic so callers have one kind of failure to render.
func ParseFile(path string, meta map[string]string) (*Parsed, hcl.Diagnostics) {
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

// Parse decodes source into a job file, returning what it decoded alongside its
// diagnostics. Required metadata is checked first, so a missing key is named.
func Parse(cfg Config) (*Parsed, hcl.Diagnostics) {
	source, diags := parseSource(cfg)
	if source == nil {
		return nil, diags
	}

	// Returned even when decoding fails, so diagnostics render against the text
	parsed := &Parsed{Filename: sourceName(cfg), Source: source}

	required, requiredDiags := RequiredMeta(source.Body)
	diags = append(diags, requiredDiags...)

	// Decoding would fail at every reference to a missing key, burying this one
	if missing := CheckRequiredMeta(required, cfg.Meta); missing.HasErrors() {
		return parsed, append(diags, missing...)
	}

	ctx := EvalContext(cfg.Meta)

	var spec job.File

	diags = append(diags, gohcl.DecodeBody(source.Body, ctx, &spec)...)
	diags = append(diags, checkConfigBlocks(&spec)...)
	diags = append(diags, bindTasks(&spec, ctx, cfg.Meta)...)

	parsed.Spec = &spec

	return parsed, diags
}

// parseSource turns bytes into an hcl file.
func parseSource(cfg Config) (*hcl.File, hcl.Diagnostics) {
	return hclsyntax.ParseConfig(cfg.Source, sourceName(cfg), hcl.InitialPos)
}

// sourceName is what diagnostics call the input. A pipe has no filename, and a
// range naming the empty string reads as a bug rather than as standard input.
func sourceName(cfg Config) string {
	if cfg.Filename == "" {
		return "<stdin>"
	}

	return cfg.Filename
}

// -------------------------------------------------------------------------
// BINDING
// -------------------------------------------------------------------------

// bindTasks gives every task what its undecoded blocks are evaluated against
// and its metadata: the job's meta block, overridden by what was supplied.
func bindTasks(file *job.File, ctx *hcl.EvalContext, supplied map[string]string) hcl.Diagnostics {
	var diags hcl.Diagnostics

	for i := range file.Jobs {
		j := &file.Jobs[i]

		meta, metaDiags := j.Meta.Attributes(ctx)
		diags = append(diags, metaDiags...)

		if meta == nil {
			meta = make(map[string]string, len(supplied))
		}

		maps.Copy(meta, supplied)

		for k := range j.Tasks {
			j.Tasks[k].Vars = ctx
			j.Tasks[k].Meta = meta

			diags = append(diags, checkReferences(&j.Tasks[k])...)
		}
	}

	return diags
}

// checkReferences evaluates a task's config and env attributes against Vars, so
// a misspelled ${meta.key} fails at parse rather than when a provider reads it.
func checkReferences(task *job.Task) hcl.Diagnostics {
	var diags hcl.Diagnostics

	if block := task.Config; block != nil && block.Body != nil {
		// Nested config blocks are reported by checkConfigBlocks.
		if attrs, attrDiags := block.Body.JustAttributes(); !attrDiags.HasErrors() {
			for _, attr := range attrs {
				if _, valueDiags := attr.Expr.Value(task.Vars); valueDiags.HasErrors() {
					diags = append(diags, valueDiags...)
				}
			}
		}
	}

	// Read as providers read it, so a non-string value fails here, not at run
	if _, envDiags := task.Env.Attributes(task.Vars); envDiags.HasErrors() {
		diags = append(diags, envDiags...)
	}

	return diags
}

// -------------------------------------------------------------------------
// CONFIG BLOCKS
// -------------------------------------------------------------------------

// checkConfigBlocks rejects nested blocks inside a task's config, which every
// consumer's JustAttributes would otherwise fail on inside a provider plugin.
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
