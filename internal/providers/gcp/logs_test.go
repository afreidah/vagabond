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
	"io"
	"strings"
	"testing"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// logsRoute is the fake Google route Cloud Logging reads go to.
const logsRoute = "POST /v2/entries:list"

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
func TestReadLogs_FollowsEmptyPages(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respondInTurn(logsRoute,
		page("shard-2"),
		page("shard-3"),
		page("", entry("hello-stdout"), entry("hello-stderr")),
	)

	var out bytes.Buffer

	if err := p.ReadLogs(t.Context(), newID(t), &out); err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	if want := "hello-stdout\nhello-stderr\n"; out.String() != want {
		t.Errorf("logs = %q, want %q", out.String(), want)
	}
}

// The token is carried forward, or the second page is the first page again and
// the scan never ends. Every page is asked for oldest first.
func TestReadLogs_SendsThePageToken(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respondInTurn(logsRoute,
		page("shard-2", entry("first")),
		page("", entry("second")),
	)

	if err := p.ReadLogs(t.Context(), newID(t), io.Discard); err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	if got := g.request(0).body["pageToken"]; got != nil {
		t.Errorf("the first request carried a token: %v", got)
	}

	if got := g.request(1).body["pageToken"]; got != "shard-2" {
		t.Errorf("second request pageToken = %v, want shard-2", got)
	}

	for i := range 2 {
		if got := g.request(i).body["orderBy"]; got != "timestamp asc" {
			t.Errorf("request %d orderBy = %v, want timestamp asc", i, got)
		}
	}
}

// A filter matching nothing anywhere would otherwise walk every shard in the
// project to say so. Stopping there is reported, after what was read.
func TestReadLogs_StopsAtThePageCap(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	// Always another page.
	g.respond(logsRoute, page("forever", entry("more")))

	var out bytes.Buffer

	err := p.ReadLogs(t.Context(), newID(t), &out)
	if !errors.Is(err, errPageCap) || classOf(err) != plugin.ClassInfrastructure {
		t.Errorf("ReadLogs() = %v, want an Infrastructure error wrapping errPageCap", err)
	}

	if len(g.requests) != maxLogPages {
		t.Errorf("made %d requests, want the cap of %d", len(g.requests), maxLogPages)
	}

	if out.String() != strings.Repeat("more\n", maxLogPages) {
		t.Errorf("wrote %q, want every page read before the cap", out.String())
	}
}

// Cloud Logging emits structural records with no text alongside the
// container's output, and a blank line each would pad a short log with noise.
func TestReadLogs_SkipsEmptyPayloads(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond(logsRoute, page("", entry(""), entry("real output"), entry("")))

	var out bytes.Buffer

	if err := p.ReadLogs(t.Context(), newID(t), &out); err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	if want := "real output\n"; out.String() != want {
		t.Errorf("logs = %q, want %q", out.String(), want)
	}
}

// The filter has to name the job, or one execution's output is stored as
// another's.
func TestReadLogs_FiltersToTheJob(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond(logsRoute, page(""))

	id := newID(t)

	if err := p.ReadLogs(t.Context(), id, io.Discard); err != nil {
		t.Fatalf("ReadLogs failed: %v", err)
	}

	filter, _ := g.request(0).body["filter"].(string)
	if !strings.Contains(filter, `job_name="`+jobName(id)+`"`) ||
		!strings.Contains(filter, `resource.type="cloud_run_job"`) {
		t.Errorf("filter = %q", filter)
	}
}

// A page Google refuses ends the read with the refusal.
func TestReadLogs_ReportsARefusedPage(t *testing.T) {
	t.Parallel()

	// Nothing registered for entries:list, so it 404s.
	_, p := newFakeGoogle(t)

	if err := p.ReadLogs(t.Context(), newID(t), io.Discard); err == nil {
		t.Fatal("a refused page was reported as success")
	}
}

// A write that fails is the caller's sink, so it is Internal.
func TestReadLogs_ReportsAFailedWrite(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond(logsRoute, page("", entry("line")))

	gone := writerFunc(func([]byte) (int, error) { return 0, errors.New("client gone") })

	if err := p.ReadLogs(t.Context(), newID(t), gone); classOf(err) != plugin.ClassInternal {
		t.Errorf("ReadLogs() = %v, want an Internal error", err)
	}
}
