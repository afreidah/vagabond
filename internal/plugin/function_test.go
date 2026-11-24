// -------------------------------------------------------------------------------
// Function Tasks Tests
//
// Author: Alex Freidah
//
// A function task read once for every function platform: the function it
// names, the event it is sent with its args, env and metadata, the task's own
// faults refused as internal, and the reply turned into the task's exit code
// and output whatever the function answered.
// -------------------------------------------------------------------------------

package plugin

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// INVOCATION
// -------------------------------------------------------------------------

// The event carries the execution, the args and the env with the job's
// metadata, and the function is the one the config names.
func TestFunctionOf(t *testing.T) {
	id := testkit.NewID(t)
	task := &job.Task{
		Name:   "lint",
		Config: block(t, "function = \"lint\"\nargs = [\"--strict\", \"a b\"]"),
		Env:    block(t, `CI = "true"`),
		Meta:   map[string]string{"commit": "3f9c2e1"},
	}

	got, err := FunctionOf(id, task)
	if err != nil {
		t.Fatalf("FunctionOf() = %v", err)
	}

	var ev event
	if err := json.Unmarshal(got.Event, &ev); err != nil {
		t.Fatalf("decoding the event %s: %v", got.Event, err)
	}

	if got.Name != "lint" || ev.ExecutionID != id.String() {
		t.Errorf("FunctionOf() = %s with %s, want lint with execution %s", got.Name, got.Event, id)
	}

	if len(ev.Args) != 2 || ev.Args[1] != "a b" || ev.Env["CI"] != "true" || ev.Env["VAGABOND_META_COMMIT"] != "3f9c2e1" {
		t.Errorf("event = %s, want both args whole, the env and the metadata", got.Event)
	}
}

// A task with no args and no env sends the execution ID alone.
func TestFunctionOf_OmitsWhatIsEmpty(t *testing.T) {
	id := testkit.NewID(t)

	got, err := FunctionOf(id, &job.Task{Name: "lint", Config: block(t, `function = "lint"`)})
	if err != nil {
		t.Fatalf("FunctionOf() = %v", err)
	}

	if want := `{"execution_id":"` + id.String() + `"}`; string(got.Event) != want {
		t.Errorf("event = %s, want %s", got.Event, want)
	}
}

// A task naming no function, or with an env that does not evaluate, is its
// own fault, which no provider would run.
func TestFunctionOf_RefusesTheTasksOwnFaults(t *testing.T) {
	function := block(t, `function = "lint"`)
	faults := map[string]*job.Task{
		"no function":    {Name: "lint", Config: block(t, `args = ["--strict"]`)},
		"empty function": {Name: "lint", Config: block(t, `function = ""`)},
		"a list env":     {Name: "lint", Config: function, Env: block(t, `CI = ["true"]`)},
	}

	for name, task := range faults {
		_, err := FunctionOf(testkit.NewID(t), task)
		if failure, ok := errors.AsType[*Error](err); !ok || failure.Class != ClassInternal {
			t.Errorf("FunctionOf() with %s = %v, want an internal failure", name, err)
		}
	}
}

// -------------------------------------------------------------------------
// REPLY
// -------------------------------------------------------------------------

// A reply's exit code decides the task, its output becoming the logs; any
// other answer fails it with exit code 1 and keeps the answer whole.
func TestReplied(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		state  execution.State
		exit   int
		output string
	}{
		{"exit zero", `{"exit_code":0,"output":"ok\n"}`, execution.StateSucceeded, 0, "ok\n"},
		{"exit non-zero", `{"exit_code":3,"output":"3 issues\n"}`, execution.StateFailed, 3, "3 issues\n"},
		{"no exit code", `{"output":"ok"}`, execution.StateFailed, 1, `{"output":"ok"}`},
		{"a platform error", `{"errorMessage":"Task timed out"}`, execution.StateFailed, 1, `{"errorMessage":"Task timed out"}`},
		{"not JSON", "Bad Gateway", execution.StateFailed, 1, "Bad Gateway"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := testkit.NewID(t)

			got := Replied(id, "lint", []byte(c.body), 2*time.Second)
			if err := got.Validate(); err != nil {
				t.Fatalf("Replied() = %+v: %v", got, err)
			}

			r := got.Result
			if got.State != c.state || got.ProviderID != "lint" || r.ID != id || r.Duration != 2*time.Second {
				t.Errorf("Replied() = %+v with %+v, want %s from lint after 2s", got, r, c.state)
			}

			if *r.ExitCode != c.exit || string(r.Logs) != c.output {
				t.Errorf("result = exit %d with %q, want exit %d with %q", *r.ExitCode, r.Logs, c.exit, c.output)
			}
		})
	}
}
