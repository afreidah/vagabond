// -------------------------------------------------------------------------------
// Cloud Logging
//
// Author: Alex Freidah
//
// Where the container's output comes from, and the reason this platform was
// chosen. A spike proved both halves: the entries are readable the moment a
// run finishes, and the free tier is 50 GiB a month against output measured in
// kilobytes.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Bounds on what a single result may carry.
//
// A test suite can print megabytes, and holding all of it to hand back a
// truncated copy would be the one place this plugin could exhaust memory on
// someone else's behalf. execution.Result already has a flag for saying so.
const (
	maxLogEntries = 1000
	maxLogBytes   = 256 * 1024
)

// maxLogPages bounds how far the scan will follow Cloud Logging's tokens.
//
// Needed because an empty page is not the end: entries.list scans storage in
// shards and returns a page per shard, so a short log arrives after two or
// three pages that contain nothing at all. A live run put its output on the
// third. Without a cap a filter matching nothing anywhere would walk every
// shard in the project to say so.
const maxLogPages = 20

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// logs returns what a job's container printed, and whether it was cut short.
//
// Both streams arrive interleaved in timestamp order, which is what a person
// reading a failed build wants: stderr next to the stdout that preceded it,
// rather than two separated blocks to correlate by eye.
func (p *Provider) logs(ctx context.Context, jobName string) ([]byte, bool, error) {
	var (
		buf   logBuffer
		token string
	)

	for range maxLogPages {
		page, err := p.logPage(ctx, jobName, token)
		if err != nil {
			return nil, false, err
		}

		for _, entry := range page.Entries {
			if !buf.add(entry.TextPayload) {
				return buf.bytes(), true, nil
			}
		}

		// The end of the scan is the token going away, not a page arriving
		// empty: entries.list scans storage in shards and returns a page per
		// shard, so the first pages of a short log are routinely empty.
		if page.NextPageToken == "" {
			return buf.bytes(), false, nil
		}

		token = page.NextPageToken
	}

	return buf.bytes(), true, nil
}

// logEntries is one page of Cloud Logging's response.
type logEntries struct {
	Entries []struct {
		TextPayload string `json:"textPayload"`
	} `json:"entries"`
	NextPageToken string `json:"nextPageToken"`
}

// logPage fetches one page of a job's output.
func (p *Provider) logPage(
	ctx context.Context, jobName, token string,
) (*logEntries, error) {
	request := map[string]any{
		"resourceNames": []string{"projects/" + p.cfg.Project},
		"filter": fmt.Sprintf(
			`resource.type="cloud_run_job" resource.labels.job_name=%q`, jobName),
		"orderBy":  "timestamp asc",
		"pageSize": maxLogEntries,
	}

	if token != "" {
		request["pageToken"] = token
	}

	var out logEntries

	url := p.logsURL + "/v2/entries:list"
	if err := p.call(ctx, http.MethodPost, url, request, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// -------------------------------------------------------------------------
// ACCUMULATION
// -------------------------------------------------------------------------

// logBuffer collects output up to the bounds a single result may carry.
type logBuffer struct {
	built   strings.Builder
	entries int
}

// add appends one entry, reporting whether there is room for more.
//
// A structural entry with no text is skipped rather than written: Cloud Logging
// emits them alongside the container's output, and a blank line per lifecycle
// event would pad a short log with noise.
func (b *logBuffer) add(text string) bool {
	if text == "" {
		return true
	}

	if b.built.Len()+len(text) > maxLogBytes {
		return false
	}

	b.built.WriteString(text)
	b.built.WriteByte('\n')

	b.entries++

	return b.entries < maxLogEntries
}

// bytes returns what was collected.
func (b *logBuffer) bytes() []byte {
	return []byte(b.built.String())
}
