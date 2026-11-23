// -------------------------------------------------------------------------------
// Cloud Logging Tests
//
// Author: Alex Freidah
//
// Paging is the whole subject here. entries.list scans storage in shards and
// returns empty pages before the one holding the output, so a reader stopping
// at the first page reports a build with no logs at all.
// -------------------------------------------------------------------------------

package gcp

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// entry is one Cloud Logging record.
func entry(text string) map[string]any {
	return map[string]any{"textPayload": text}
}

// page is one response, with or without a continuation.
func page(token string, entries ...any) map[string]any {
	out := map[string]any{"entries": entries}
	if token != "" {
		out["nextPageToken"] = token
	}

	return out
}

// read runs ReadEntries against f and returns what it wrote.
func read(t *testing.T, f *fakeLogging) (string, error) {
	t.Helper()

	var out bytes.Buffer

	err := newLogging(t, f).ReadEntries(t.Context(), testFilter, &execution.PlacedWriter{Writer: &out})

	return out.String(), err
}

// classOf returns the class err carries, empty when it carries none.
func classOf(err error) plugin.Class {
	if classified, ok := errors.AsType[*plugin.Error](err); ok {
		return classified.Class
	}

	return ""
}

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// An empty page is not the end of the scan.
func TestReadEntries_FollowsEmptyPages(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{pages: []any{
		page("shard-2"),
		page("shard-3"),
		page("", entry("hello-stdout"), entry("hello-stderr")),
	}}

	if got, err := read(t, f); err != nil || got != "hello-stdout\nhello-stderr\n" {
		t.Errorf("ReadEntries() = %q, %v; want both lines", got, err)
	}
}

// The token is carried forward, or the second page is the first page again and
// the scan never ends. Every page is asked for oldest first, in the project,
// with the filter it was given.
func TestReadEntries_SendsThePageToken(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{pages: []any{page("shard-2", entry("first")), page("", entry("second"))}}

	if _, err := read(t, f); err != nil {
		t.Fatalf("ReadEntries failed: %v", err)
	}

	if got := f.lists[0]["pageToken"]; got != nil {
		t.Errorf("the first request carried a token: %v", got)
	}

	if got := f.lists[1]["pageToken"]; got != "shard-2" {
		t.Errorf("second request pageToken = %v, want shard-2", got)
	}

	for i, request := range f.lists {
		if request["orderBy"] != "timestamp asc" || request["filter"] != testFilter ||
			fmt.Sprint(request["resourceNames"]) != "[projects/test-project]" {
			t.Errorf("request %d = %v, want the filter, project and order", i, request)
		}
	}
}

// A filter matching nothing anywhere would otherwise walk every shard in the
// project to say so. Stopping there is reported, after what was read.
func TestReadEntries_StopsAtThePageCap(t *testing.T) {
	t.Parallel()

	// Always another page.
	f := &fakeLogging{pages: []any{page("forever", entry("more"))}}

	got, err := read(t, f)
	if !errors.Is(err, errPageCap) || classOf(err) != plugin.ClassInfrastructure {
		t.Errorf("ReadEntries() = %v, want an Infrastructure error wrapping errPageCap", err)
	}

	if len(f.lists) != maxLogPages {
		t.Errorf("made %d requests, want the cap of %d", len(f.lists), maxLogPages)
	}

	if got != strings.Repeat("more\n", maxLogPages) {
		t.Errorf("wrote %q, want every page read before the cap", got)
	}
}

// Cloud Logging emits structural records with no text alongside the
// container's output, and a blank line each would pad a short log with noise.
func TestReadEntries_SkipsEmptyPayloads(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{pages: []any{page("", entry(""), entry("real output"), entry(""))}}

	if got, err := read(t, f); err != nil || got != "real output\n" {
		t.Errorf("ReadEntries() = %q, %v; want only the real output", got, err)
	}
}

// A page Google refuses ends the read with the refusal.
func TestReadEntries_ReportsARefusedPage(t *testing.T) {
	t.Parallel()

	if _, err := read(t, &fakeLogging{listStatus: http.StatusTooManyRequests}); classOf(err) != plugin.ClassInfrastructure {
		t.Errorf("ReadEntries() = %v, want the refusal, rerouted", err)
	}
}

// A write that fails is the caller's sink, so it is Internal.
func TestReadEntries_ReportsAFailedWrite(t *testing.T) {
	t.Parallel()

	f := &fakeLogging{pages: []any{page("", entry("line"))}}
	gone := writerFunc(func([]byte) (int, error) { return 0, errors.New("client gone") })

	err := newLogging(t, f).ReadEntries(t.Context(), testFilter, &execution.PlacedWriter{Writer: gone})
	if classOf(err) != plugin.ClassInternal {
		t.Errorf("ReadEntries() = %v, want an Internal error", err)
	}
}
