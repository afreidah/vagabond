// -------------------------------------------------------------------------------
// Cloud Logging Tests
//
// Author: Alex Freidah
//
// Paging is the whole subject here. A live run against a real project returned
// two empty pages before the one holding the output, which is what entries.list
// does when it scans storage in shards, and a reader that stopped at the first
// page reported a build with no logs at all.
// -------------------------------------------------------------------------------

package gcp

import (
	"strings"
	"testing"
)

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

// The bug a live run found: an empty page is not the end of the scan.
func TestLogsFollowsEmptyPages(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respondInTurn(logsRoute,
		page("shard-2"),
		page("shard-3"),
		page("", entry("hello-stdout"), entry("hello-stderr")),
	)

	logs, truncated, err := p.logs(t.Context(), "vagabond-abc")
	if err != nil {
		t.Fatalf("logs failed: %v", err)
	}

	if truncated {
		t.Error("a complete log was reported as truncated")
	}

	if want := "hello-stdout\nhello-stderr\n"; string(logs) != want {
		t.Errorf("logs = %q, want %q", logs, want)
	}
}

// The token is carried forward, or the second page is the first page again and
// the scan never ends.
func TestLogsSendsThePageToken(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respondInTurn(logsRoute,
		page("shard-2", entry("first")),
		page("", entry("second")),
	)

	if _, _, err := p.logs(t.Context(), "vagabond-abc"); err != nil {
		t.Fatalf("logs failed: %v", err)
	}

	if got := g.request(0).body["pageToken"]; got != nil {
		t.Errorf("the first request carried a token: %v", got)
	}

	if got := g.request(1).body["pageToken"]; got != "shard-2" {
		t.Errorf("second request pageToken = %v, want shard-2", got)
	}
}

// A filter matching nothing anywhere would otherwise walk every shard in the
// project to say so.
func TestLogsStopsAtThePageCap(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	// Always another page, never any content.
	g.respond(logsRoute, page("forever"))

	_, truncated, err := p.logs(t.Context(), "vagabond-abc")
	if err != nil {
		t.Fatalf("logs failed: %v", err)
	}

	if !truncated {
		t.Error("giving up early was not reported as truncated")
	}

	if len(g.requests) != maxLogPages {
		t.Errorf("made %d requests, want the cap of %d", len(g.requests), maxLogPages)
	}
}

// Cloud Logging emits structural records with no text alongside the
// container's output, and a blank line each would pad a short log with noise.
func TestLogsSkipsEmptyPayloads(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond(logsRoute, page("", entry(""), entry("real output"), entry("")))

	logs, _, err := p.logs(t.Context(), "vagabond-abc")
	if err != nil {
		t.Fatalf("logs failed: %v", err)
	}

	if want := "real output\n"; string(logs) != want {
		t.Errorf("logs = %q, want %q", logs, want)
	}
}

// A test suite can print megabytes, and holding all of it to hand back a
// truncated copy is the one place this plugin could exhaust memory.
func TestLogsTruncatesAtTheByteLimit(t *testing.T) {
	t.Parallel()

	line := strings.Repeat("x", 64*1024)

	g, p := newFakeGoogle(t)
	g.respond(logsRoute, page("", entry(line), entry(line), entry(line),
		entry(line), entry(line), entry(line)))

	logs, truncated, err := p.logs(t.Context(), "vagabond-abc")
	if err != nil {
		t.Fatalf("logs failed: %v", err)
	}

	if !truncated {
		t.Error("an oversized log was not reported as truncated")
	}

	if len(logs) > maxLogBytes {
		t.Errorf("kept %d bytes, over the %d limit", len(logs), maxLogBytes)
	}
}

// The filter has to name the job, or one execution's result carries another's
// output.
func TestLogsFiltersToTheJob(t *testing.T) {
	t.Parallel()

	g, p := newFakeGoogle(t)
	g.respond(logsRoute, page(""))

	if _, _, err := p.logs(t.Context(), "vagabond-abc"); err != nil {
		t.Fatalf("logs failed: %v", err)
	}

	filter, _ := g.request(0).body["filter"].(string)
	if !strings.Contains(filter, `job_name="vagabond-abc"`) ||
		!strings.Contains(filter, `resource.type="cloud_run_job"`) {
		t.Errorf("filter = %q", filter)
	}
}
