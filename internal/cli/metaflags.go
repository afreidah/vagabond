// -------------------------------------------------------------------------------
// The -meta and -label Flags
//
// Author: Alex Freidah
//
// Collects repeated key=value pairs into a map: -meta on the job commands, so
// every one of them accepts the same spelling, and -label on the agent.
// -------------------------------------------------------------------------------

package cli

import (
	"fmt"
	"strings"
)

// metaFlags accumulates repeated key=value arguments. As a flag.Value it is
// repeatable, since the flag package calls Set once per occurrence.
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

// Set records one key=value pair. A repeated key is an error rather than a
// silent overwrite, which would hide which value the submission ran with.
func (m *metaFlags) Set(value string) error {
	key, val, found := strings.Cut(value, "=")
	if !found {
		return fmt.Errorf("expected key=value, got %q", value)
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("key is empty in %q", value)
	}

	if *m == nil {
		*m = make(map[string]string)
	}

	if _, exists := (*m)[key]; exists {
		return fmt.Errorf("key %q was given more than once", key)
	}

	(*m)[key] = val

	return nil
}
