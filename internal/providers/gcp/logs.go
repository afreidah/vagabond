// -------------------------------------------------------------------------------
// Cloud Logging
//
// Author: Alex Freidah
//
// Where the container's output comes from, and the reason this platform was
// chosen: entries are readable the moment a run finishes, and the free tier is
// 50 GiB a month against output measured in kilobytes.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Bounds on what a single result may carry, so a test suite printing megabytes
// cannot exhaust memory here. execution.Result flags the truncation.
const (
	maxLogEntries = 1000
	maxLogBytes   = 256 * 1024
)

// maxLogPages bounds how far the scan follows Cloud Logging's tokens. Pages
// arrive one per storage shard, often empty, so without a cap a filter matching
// nothing walks every shard in the project.
const maxLogPages = 20

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// logs returns what a job's container printed, and whether it was cut short.
// Both streams arrive interleaved in timestamp order, as a failed build reads.
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

		// The scan ends when the token goes away; an empty page is routine
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

// add appends one entry, reporting whether there is room for more. An entry
// with no text is one of Cloud Logging's structural ones, and is skipped.
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
