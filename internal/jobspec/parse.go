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

// Parsed is a decoded specification together with the source it came from.
//
// Source is carried so that diagnostics can be rendered against the original
// text, with the offending line shown underneath. Without it a caller holds a
// range naming a file and a line but no way to display either.
type Parsed struct {
	Spec     *job.File
	Filename string
	Source   *hcl.File
}

// Files returns the source in the shape hcl's diagnostic writer expects.
//
// Safe on a nil receiver and on a parse that produced nothing, because the
// caller rendering diagnostics is usually the caller whose parse just failed.
func (p *Parsed) Files() map[string]*hcl.File {
	if p == nil || p.Source == nil {
		return nil
	}

	return map[string]*hcl.File{p.Filename: p.Source}
}

// -------------------------------------------------------------------------
// PARSING
// -------------------------------------------------------------------------

// ParseFile reads and parses the named file.
//
// A read failure is reported as a diagnostic rather than an error so that
// callers have one kind of failure to render, whether the file was unreadable
// or merely wrong.
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

// Parse decodes source into a job file.
//
// Returns whatever it managed to decode alongside its diagnostics, because a
// file with one bad attribute is still worth reporting the rest of.
//
// Required metadata is checked before decoding rather than after. A job that
// declares meta_required and was submitted without it fails naming the keys,
// instead of failing at whichever expression happened to reference one first.
func Parse(cfg Config) (*Parsed, hcl.Diagnostics) {
	source, diags := parseSource(cfg)
	if source == nil {
		return nil, diags
	}

	// Returned even when decoding fails, so that a caller can render the
	// diagnostics against the text they came from.
	parsed := &Parsed{Filename: sourceName(cfg), Source: source}

	required, requiredDiags := RequiredMeta(source.Body)
	diags = append(diags, requiredDiags...)

	// Stop here when metadata is missing. Decoding would go on to fail at every
	// expression that references one of those keys, burying the one diagnostic
	// that says what to do under several that repeat it obliquely.
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

// sourceName is what diagnostics call the input.
//
// Reading from a pipe has no filename, and a range naming the empty string
// reads as a bug rather than as standard input.
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
// and its metadata: the job's meta block, overridden by what was supplied, as
// Nomad merges job meta with dispatch meta.
func bindTasks(file *job.File, ctx *hcl.EvalContext, supplied map[string]string) hcl.Diagnostics {
	var diags hcl.Diagnostics

	for i := range file.Jobs {
		j := &file.Jobs[i]

		meta, metaDiags := j.Meta.Attributes(ctx)
		diags = append(diags, metaDiags...)

		if meta == nil {
			meta = make(map[string]string, len(supplied))
		}

		for key, value := range supplied {
			meta[key] = value
		}

		for k := range j.Tasks {
			j.Tasks[k].Vars = ctx
			j.Tasks[k].Meta = meta

			diags = append(diags, checkReferences(&j.Tasks[k])...)
		}
	}

	return diags
}

// checkReferences evaluates every attribute of a task's config and env blocks
// against its Vars. Both are left undecoded for the driver, so without this a
// misspelled ${meta.key} in either would surface only when a provider read it.
func checkReferences(task *job.Task) hcl.Diagnostics {
	var diags hcl.Diagnostics

	for _, block := range []*job.RawBlock{task.Config, task.Env} {
		if block == nil || block.Body == nil {
			continue
		}

		attrs, attrDiags := block.Body.JustAttributes()
		if attrDiags.HasErrors() {
			// Nested config blocks are reported by checkConfigBlocks.
			continue
		}

		for _, attr := range attrs {
			if _, valueDiags := attr.Expr.Value(task.Vars); valueDiags.HasErrors() {
				diags = append(diags, valueDiags...)
			}
		}
	}

	return diags
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
