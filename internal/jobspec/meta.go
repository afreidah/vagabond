// -------------------------------------------------------------------------------
// Job Metadata
//
// Author: Alex Freidah
//
// A job declares the metadata a submission must supply and references those
// values through the meta namespace; a key is a name and a value is a string.
// A referenced value nobody supplied is an error rather than a placeholder, so
// a job cannot validate and then interpolate nothing at submission.
// -------------------------------------------------------------------------------

package jobspec

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// MetaNamespace is the accessor a job file uses to reach submission metadata,
// as in meta.version, named to match parameterized.meta_required.
const MetaNamespace = "meta"

// -------------------------------------------------------------------------
// EVALUATION CONTEXT
// -------------------------------------------------------------------------

// EvalContext builds the context job expressions are evaluated against. Only
// supplied metadata is registered, so any other reference fails at its range.
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

// metaObject wraps supplied metadata as an object value, empty rather than null
// so a bad reference fails naming the missing key rather than nullness.
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
var requiredMetaSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{{Type: "job", LabelNames: []string{"name"}}},
}

var parameterizedSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{{Type: "parameterized"}},
}

var metaRequiredSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "meta_required"}, {Name: "meta_optional"}},
}

// Requirement is one job's declared metadata and where it declared it, so a
// message about a missing key points at the line that asked for it.
type Requirement struct {
	Job   string
	Keys  []string
	Range hcl.Range
}

// RequiredMeta returns what every job in the body declares, in file order so a
// caller's report follows the file.
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

// stringList evaluates a list-of-strings attribute against a nil context, since
// a declaration of the metadata a job needs cannot itself depend on metadata.
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
				Detail:   fmt.Sprintf("Every entry in %s must be a string.", attr.Name),
				Subject:  attr.Expr.Range().Ptr(),
			})

			continue
		}

		keys = append(keys, element.AsString())
	}

	return keys
}

// -------------------------------------------------------------------------
// DECLARED METADATA
// -------------------------------------------------------------------------

// Declaration is what a job's parameterized block says a submission may
// supply. Parameterized is false for a job with no such block, which takes no
// metadata at dispatch at all.
type Declaration struct {
	Job           string
	Parameterized bool
	Required      []string
	Optional      []string
}

// Declared reads the first job's declaration from source, before anything is
// evaluated.
func Declared(filename string, source []byte) (Declaration, hcl.Diagnostics) {
	file, diags := parseSource(Config{Filename: filename, Source: source})
	if file == nil {
		return Declaration{}, diags
	}

	content, _, contentDiags := file.Body.PartialContent(requiredMetaSchema)
	diags = append(diags, contentDiags...)

	if content == nil || len(content.Blocks) == 0 {
		return Declaration{}, diags
	}

	block := content.Blocks[0]
	d := Declaration{}

	if len(block.Labels) > 0 {
		d.Job = block.Labels[0]
	}

	params, _, paramDiags := block.Body.PartialContent(parameterizedSchema)
	diags = append(diags, paramDiags...)

	if params == nil || len(params.Blocks) == 0 {
		return d, diags
	}

	d.Parameterized = true

	attrs, _, attrDiags := params.Blocks[0].Body.PartialContent(metaRequiredSchema)
	diags = append(diags, attrDiags...)

	if attrs == nil {
		return d, diags
	}

	if attr, ok := attrs.Attributes["meta_required"]; ok {
		d.Required = stringList(attr, &diags)
	}

	if attr, ok := attrs.Attributes["meta_optional"]; ok {
		d.Optional = stringList(attr, &diags)
	}

	return d, diags
}

// References binds each declared key to its own ${meta.key} text, so a job
// validates at register while an undeclared reference still fails.
func (d Declaration) References() map[string]string {
	refs := make(map[string]string, len(d.Required)+len(d.Optional))

	for _, key := range append(append([]string(nil), d.Required...), d.Optional...) {
		refs[key] = "${" + MetaNamespace + "." + key + "}"
	}

	return refs
}

// CheckDispatch refuses metadata a dispatch may not supply: any for a job not
// parameterized, keys in neither list, and missing required keys.
func (d Declaration) CheckDispatch(supplied map[string]string) error {
	if !d.Parameterized {
		if len(supplied) > 0 {
			return fmt.Errorf("job %q is not parameterized, so dispatch takes no metadata", d.Job)
		}

		return nil
	}

	var unpermitted []string

	for key := range supplied {
		if !slices.Contains(d.Required, key) && !slices.Contains(d.Optional, key) {
			unpermitted = append(unpermitted, key)
		}
	}

	if len(unpermitted) > 0 {
		slices.Sort(unpermitted)

		return fmt.Errorf("job %q does not declare metadata %s; declare it in meta_required or meta_optional",
			d.Job, strings.Join(unpermitted, ", "))
	}

	if missing := missingKeys(d.Required, supplied); len(missing) > 0 {
		return fmt.Errorf("job %q requires metadata that was not supplied: %s",
			d.Job, strings.Join(missing, ", "))
	}

	return nil
}

// -------------------------------------------------------------------------
// ENFORCEMENT
// -------------------------------------------------------------------------

// CheckRequiredMeta reports metadata a job declared that the caller did not
// supply, so a submission fails before any provider is contacted.
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
