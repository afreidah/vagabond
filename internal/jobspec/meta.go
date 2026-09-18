// -------------------------------------------------------------------------------
// Job Metadata
// -------------------------------------------------------------------------------
//
// Author: Alex Freidah
//
// A job declares the metadata a submission must supply, and references those
// values through the meta namespace. Vagabond neither knows nor cares what any
// of them mean: a key is a name and a value is a string, and what a job does
// with them is the job's business.
//
// A referenced value that nobody supplied is an error, matching Nomad, whose
// Variable.Value reports "Unset variable" for the same situation. The
// alternative, carrying a placeholder so validation can pass, means a job can
// validate and then interpolate nothing at submission, which is worse than
// being told to supply the value.
// -------------------------------------------------------------------------------

package jobspec

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// MetaNamespace is the accessor a job file uses to reach submission metadata,
// as in meta.version.
//
// Nomad namespaces its own accessors the same way, with var and local. The name
// matches parameterized.meta_required naming the same keys, so a reader does
// not have to learn that two spellings mean one thing.
const MetaNamespace = "meta"

// -------------------------------------------------------------------------
// EVALUATION CONTEXT
// -------------------------------------------------------------------------

// EvalContext builds the context job expressions are evaluated against.
//
// Only supplied metadata is registered. A reference to anything else fails
// during evaluation, with a range pointing at the reference, which is what
// makes an unsupplied value and a misspelled one both reportable.
func EvalContext(meta map[string]string) *hcl.EvalContext {
	values := make(map[string]cty.Value, len(meta))
	for key, value := range meta {
		values[key] = cty.StringVal(value)
	}

	return &hcl.EvalContext{
		Variables: map[string]cty.Value{
			MetaNamespace: metaObject(values),
		},
	}
}

// metaObject wraps supplied metadata as an object value.
//
// An empty map becomes an empty object rather than a null, because a reference
// into a null fails with a message about nullness rather than about a missing
// key, and the second is the one an author can act on.
func metaObject(values map[string]cty.Value) cty.Value {
	if len(values) == 0 {
		return cty.EmptyObjectVal
	}

	return cty.ObjectVal(values)
}

// -------------------------------------------------------------------------
// REQUIRED METADATA
// -------------------------------------------------------------------------

// requiredMetaSchema matches the blocks that declare required metadata, without
// decoding anything that could reference a value.
//
// Extracting this before evaluation is what lets a missing key be reported as
// itself rather than as whatever expression happened to reference it first.
var requiredMetaSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{{Type: "job", LabelNames: []string{"name"}}},
}

var parameterizedSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{{Type: "parameterized"}},
}

var metaRequiredSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "meta_required"}},
}

// Requirement is one job's declared metadata, and where it declared it.
//
// The range matters as much as the keys: a message about metadata nobody
// supplied should point at the line that asked for it, not at whichever
// expression happened to reference one first.
type Requirement struct {
	Job   string
	Keys  []string
	Range hcl.Range
}

// RequiredMeta returns what every job in the body declares, in file order.
//
// A slice rather than a map keyed by job name, so that the report a caller
// eventually prints follows the file rather than an arbitrary iteration order.
func RequiredMeta(body hcl.Body) ([]Requirement, hcl.Diagnostics) {
	content, _, diags := body.PartialContent(requiredMetaSchema)
	if content == nil {
		return nil, diags
	}

	required := make([]Requirement, 0, len(content.Blocks))

	for _, block := range content.Blocks {
		name := ""
		if len(block.Labels) > 0 {
			name = block.Labels[0]
		}

		keys, declRange, keyDiags := jobRequiredMeta(block.Body)
		diags = append(diags, keyDiags...)

		if len(keys) == 0 {
			continue
		}

		required = append(required, Requirement{
			Job:   name,
			Keys:  keys,
			Range: declRange,
		})
	}

	return required, diags
}

// jobRequiredMeta reads meta_required out of one job's parameterized block,
// along with the range of the declaration itself.
func jobRequiredMeta(body hcl.Body) ([]string, hcl.Range, hcl.Diagnostics) {
	content, _, diags := body.PartialContent(parameterizedSchema)
	if content == nil || len(content.Blocks) == 0 {
		return nil, hcl.Range{}, diags
	}

	attrs, _, attrDiags := content.Blocks[0].Body.PartialContent(metaRequiredSchema)
	diags = append(diags, attrDiags...)

	if attrs == nil {
		return nil, hcl.Range{}, diags
	}

	attr, ok := attrs.Attributes["meta_required"]
	if !ok {
		return nil, hcl.Range{}, diags
	}

	keys := stringList(attr, &diags)

	return keys, attr.Range, diags
}

// stringList evaluates an attribute expected to hold a list of strings.
//
// Evaluated against a nil context on purpose: a job declaring which metadata it
// needs cannot itself depend on metadata, and allowing it to would make the
// requirement unknowable until the values it names were already supplied.
func stringList(attr *hcl.Attribute, diags *hcl.Diagnostics) []string {
	value, valueDiags := attr.Expr.Value(nil)
	*diags = append(*diags, valueDiags...)

	if value.IsNull() || !value.IsKnown() || !value.CanIterateElements() {
		return nil
	}

	var keys []string

	for iter := value.ElementIterator(); iter.Next(); {
		_, element := iter.Element()

		if element.Type() != cty.String || element.IsNull() {
			*diags = append(*diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid metadata key",
				Detail:   "Every entry in meta_required must be a string.",
				Subject:  attr.Expr.Range().Ptr(),
			})

			continue
		}

		keys = append(keys, element.AsString())
	}

	return keys
}

// -------------------------------------------------------------------------
// ENFORCEMENT
// -------------------------------------------------------------------------

// CheckRequiredMeta reports metadata a job declared that the caller did not
// supply.
//
// Matches Nomad, which refuses a job referencing an unset variable rather than
// substituting a placeholder. Catching it here means a submission fails before
// any provider is contacted and any free-tier capacity is spent.
func CheckRequiredMeta(required []Requirement, supplied map[string]string) hcl.Diagnostics {
	var diags hcl.Diagnostics

	for _, req := range required {
		missing := missingKeys(req.Keys, supplied)
		if len(missing) == 0 {
			continue
		}

		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("Unset metadata for job %q", req.Job),
			Detail: fmt.Sprintf(
				"This job requires metadata that was not supplied: %s. "+
					"Pass each one with -meta <key>=<value>.",
				strings.Join(missing, ", ")),
			Subject: req.Range.Ptr(),
		})
	}

	return diags
}

// missingKeys returns the required keys absent from supplied, in declaration
// order so that an author reading the message sees them as they wrote them.
func missingKeys(required []string, supplied map[string]string) []string {
	var missing []string

	for _, key := range required {
		if _, ok := supplied[key]; !ok {
			missing = append(missing, key)
		}
	}

	return missing
}
