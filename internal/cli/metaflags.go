// -------------------------------------------------------------------------------
// The -meta Flag
//
// Author: Alex Freidah
//
// Collects repeated -meta key=value pairs into a map. Declared here rather than
// inside a command because job validate, plan, and run all take it, and a
// parameterized job is only useful if every one of them accepts the same
// spelling.
// -------------------------------------------------------------------------------

package cli

import (
	"fmt"
	"strings"
)

// metaFlags accumulates repeated -meta key=value arguments.
//
// Implements flag.Value, which is what makes the flag repeatable: the standard
// flag package calls Set once per occurrence rather than overwriting.
type metaFlags map[string]string

// String renders the collected pairs, sorted by nothing in particular because
// the flag package only calls this for usage output.
func (m *metaFlags) String() string {
	if m == nil || len(*m) == 0 {
		return ""
	}

	pairs := make([]string, 0, len(*m))
	for k, v := range *m {
		pairs = append(pairs, k+"="+v)
	}

	return strings.Join(pairs, ",")
}

// Set records one key=value pair.
//
// A repeated key is an error rather than a silent overwrite. A caller passing
// the same key twice has made a mistake somewhere upstream, and quietly taking
// the last one hides which submission actually ran.
func (m *metaFlags) Set(value string) error {
	key, val, found := strings.Cut(value, "=")
	if !found {
		return fmt.Errorf("expected key=value, got %q", value)
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("metadata key is empty in %q", value)
	}

	if *m == nil {
		*m = make(map[string]string)
	}

	if _, exists := (*m)[key]; exists {
		return fmt.Errorf("metadata key %q was given more than once", key)
	}

	(*m)[key] = val

	return nil
}
