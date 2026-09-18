// -------------------------------------------------------------------------------
// Evaluation Context
//
// Author: Alex Freidah
//
// What a job file's expressions are evaluated against. One namespace for now,
// meta, holding the values a caller supplied at submission.
//
// Metadata a job references but nobody supplied evaluates to unknown rather
// than failing. A job definition is valid without the values a particular
// submission would provide, so validating one in CI, where the commit is not
// known until the run starts, has to be possible.
// -------------------------------------------------------------------------------

package jobspec

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// MetaNamespace is the accessor a job file uses to reach submission metadata,
// as in meta.git_ref.
//
// Nomad namespaces its own accessors the same way, with var and local. The name
// matches parameterized.meta_required naming the same keys, which is the point:
// a reader should not have to learn that two spellings mean one thing.
const MetaNamespace = "meta"

// EvalContext builds the context job expressions are evaluated against.
//
// Supplied metadata becomes a known string. Anything else the job references
// under meta is left for the undefined-variable handler, which decides between
// unknown and an error depending on whether the job declared it.
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
// An empty map has to become an empty object rather than a null, because
// cty.ObjectVal of nothing is still an object and a reference into a null value
// fails with a message about nullness rather than about a missing key.
func metaObject(values map[string]cty.Value) cty.Value {
	if len(values) == 0 {
		return cty.EmptyObjectVal
	}

	return cty.ObjectVal(values)
}
