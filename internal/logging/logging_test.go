// -------------------------------------------------------------------------------
// Logging Tests
//
// Author: Alex Freidah
//
// The flags, both formats, the level, durations written as text, the component
// name, and context fields reaching every line without leaking between
// contexts derived from one.
// -------------------------------------------------------------------------------

package logging

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// OPTIONS
// -------------------------------------------------------------------------

// The flags default to info in text, and parse a level and -log-json.
func TestOptions_Register(t *testing.T) {
	t.Parallel()

	var defaults Options

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	defaults.Register(flags)

	if err := flags.Parse(nil); err != nil || defaults.Level != slog.LevelInfo || defaults.JSON {
		t.Fatalf("defaults = %+v, %v; want info in text", defaults, err)
	}

	var set Options

	flags = flag.NewFlagSet("test", flag.ContinueOnError)
	set.Register(flags)

	if err := flags.Parse([]string{"-log-level", "WARN", "-log-json"}); err != nil {
		t.Fatal(err)
	}

	if set.Level != slog.LevelWarn || !set.JSON || flags.Lookup("log-level").Value.String() != "warn" {
		t.Errorf("parsed = %+v, want warn in JSON", set)
	}
}

// A level other than the four names is refused, one slog would read included.
func TestOptions_RegisterRefusesOtherLevels(t *testing.T) {
	t.Parallel()

	for _, level := range []string{"loud", "info+2", ""} {
		var o Options

		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		o.Register(flags)

		err := flags.Parse([]string{"-log-level", level})
		if err == nil || !strings.Contains(err.Error(), "use debug, info, warn or error") {
			t.Errorf("-log-level %q: %v, want refused", level, err)
		}
	}
}

// -------------------------------------------------------------------------
// LOGGERS
// -------------------------------------------------------------------------

// A JSON line carries the component, the line's own fields, the context's,
// and a duration as text.
func TestNew_JSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	ctx := With(With(t.Context(), "dispatch_id", "d1"), "execution_id", "e1")
	Named(New(&buf, Options{JSON: true}), "dispatch").InfoContext(ctx, "dispatch finished", "took", 1500*time.Millisecond)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("%v in %q", err, buf.String())
	}

	want := map[string]any{
		"level": "INFO", "msg": "dispatch finished", "component": "dispatch",
		"took": "1.5s", "dispatch_id": "d1", "execution_id": "e1",
	}

	for key, value := range want {
		if line[key] != value {
			t.Errorf("%s = %v, want %v in %q", key, line[key], value, buf.String())
		}
	}
}

// Text is the default, and a line below the level is not written.
func TestNew_TextAtLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger := New(&buf, Options{Level: slog.LevelWarn})
	logger.InfoContext(t.Context(), "below the level")
	logger.WarnContext(With(t.Context(), "node", "n1"), "node disconnected")

	if got := buf.String(); strings.Contains(got, "below the level") ||
		!strings.Contains(got, `level=WARN msg="node disconnected" node=n1`) {
		t.Errorf("logged %q, want only the warning with its context field", got)
	}
}

// Fields added to the logger come before the context's, and a group the
// logger opens holds the context's fields too.
func TestNew_AttrsAndGroups(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	ctx := With(t.Context(), "execution_id", "e1")
	New(&buf, Options{}).With("pool", "lab").WithGroup("task").InfoContext(ctx, "started", "name", "build")

	if got, want := buf.String(), "pool=lab task.name=build task.execution_id=e1"; !strings.Contains(got, want) {
		t.Errorf("logged %q, want it to contain %q", got, want)
	}
}

// A nil logger named is one that writes nothing.
func TestNamed_NilDiscards(t *testing.T) {
	t.Parallel()

	if logger := Named(nil, "server"); logger.Enabled(t.Context(), slog.LevelError) {
		t.Error("a nil logger named writes errors")
	}
}

// -------------------------------------------------------------------------
// CONTEXT FIELDS
// -------------------------------------------------------------------------

// Two contexts derived from one each carry the parent's fields and their own,
// never each other's, even when the parent's fields have room to grow.
func TestWith_SiblingsStayApart(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger := New(&buf, Options{})

	// Three appends leave the parent's fields with spare capacity
	parent := With(With(With(t.Context(), "namespace", "default"), "job", "build"), "dispatch_id", "d1")
	first := With(parent, "execution_id", "e1")
	second := With(parent, "execution_id", "e2")

	logger.InfoContext(first, "first")

	if got := buf.String(); !strings.Contains(got, "dispatch_id=d1 execution_id=e1") || strings.Contains(got, "e2") {
		t.Errorf("first logged %q", got)
	}

	buf.Reset()
	logger.InfoContext(second, "second")

	if got := buf.String(); !strings.Contains(got, "dispatch_id=d1 execution_id=e2") || strings.Contains(got, "e1") {
		t.Errorf("second logged %q", got)
	}
}
