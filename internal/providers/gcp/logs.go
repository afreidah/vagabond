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
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// pageSize is how many entries one page of entries:list asks for.
const pageSize = 1000

// maxLogPages bounds how far a scan follows Cloud Logging's tokens. Pages
// arrive one per storage shard, often empty, so without a cap a filter matching
// nothing walks every shard in the project. Each page is one request against
// a quota of 60 a minute per project.
const maxLogPages = 20

// errPageCap reports a scan that stopped at maxLogPages with pages left, so
// the output it saw is incomplete.
var errPageCap = errors.New("stopped at the page cap with output left unread")

// -------------------------------------------------------------------------
// ENTRIES
// -------------------------------------------------------------------------

// logEntry is one entry of a job's output, as entries:list and entries:tail
// both return it. Timestamp and InsertID together identify it.
type logEntry struct {
	TextPayload string `json:"textPayload"`
	Timestamp   string `json:"timestamp"`
	InsertID    string `json:"insertId"`
}

// entryKey identifies one entry: Cloud Logging treats two with the same
// timestamp and insertId as the same entry.
type entryKey struct {
	timestamp string
	insertID  string
}

// key is the entry's identity.
func (e *logEntry) key() entryKey {
	return entryKey{timestamp: e.Timestamp, insertID: e.InsertID}
}

// logQuery is the part of a list or tail request that picks out a job's
// output.
func (p *Provider) logQuery(job string) map[string]any {
	return map[string]any{
		"resourceNames": []string{"projects/" + p.cfg.Project},
		"filter": fmt.Sprintf(
			`resource.type="cloud_run_job" resource.labels.job_name=%q`, job),
	}
}

// writeEntry writes an entry's text on its own line. An entry with no text is
// one of Cloud Logging's structural ones, and is skipped.
func writeEntry(w io.Writer, entry *logEntry) error {
	if entry.TextPayload == "" {
		return nil
	}

	_, err := io.WriteString(w, entry.TextPayload+"\n")

	return err
}

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// ReadLogs writes what a finished job's container printed. Both streams arrive
// interleaved in timestamp order, as a failed build reads. The entries outlive
// the job, so Release destroys nothing this reads.
func (p *Provider) ReadLogs(ctx context.Context, id execution.ID, w io.Writer) error {
	out := plugin.LogOutput(w)

	err := p.listEntries(ctx, jobName(id), func(entry *logEntry) error { return writeEntry(out, entry) })
	if errors.Is(err, errPageCap) {
		return plugin.Infrastructure(err)
	}

	return err
}

// listEntries calls each on a job's entries, oldest first, until the entries
// run out or each fails. Stopping at maxLogPages returns errPageCap.
func (p *Provider) listEntries(ctx context.Context, job string, each func(*logEntry) error) error {
	var token string

	for range maxLogPages {
		page, err := p.logPage(ctx, job, token)
		if err != nil {
			return err
		}

		for i := range page.Entries {
			if err := each(&page.Entries[i]); err != nil {
				return err
			}
		}

		// The scan ends when the token goes away; an empty page is routine
		if page.NextPageToken == "" {
			return nil
		}

		token = page.NextPageToken
	}

	return errPageCap
}

// logPage is one page of Cloud Logging's response.
type logPage struct {
	Entries       []logEntry `json:"entries"`
	NextPageToken string     `json:"nextPageToken"`
}

// logPage fetches one page of a job's output.
func (p *Provider) logPage(ctx context.Context, job, token string) (*logPage, error) {
	request := p.logQuery(job)
	request["orderBy"] = "timestamp asc"
	request["pageSize"] = pageSize

	if token != "" {
		request["pageToken"] = token
	}

	var out logPage

	if err := p.call(ctx, http.MethodPost, p.logsURL+"/v2/entries:list", request, &out); err != nil {
		return nil, err
	}

	return &out, nil
}
