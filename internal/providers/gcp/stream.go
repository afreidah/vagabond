// -------------------------------------------------------------------------------
// Log Streaming
//
// Author: Alex Freidah
//
// entries:tail, a bidirectional streaming method reachable over plain HTTP
// through gRPC transcoding. The request body is a JSON array because the body
// is a stream of requests, and the response is a JSON array that stays open
// rather than one object per line.
// -------------------------------------------------------------------------------

package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// bufferWindow is how long Cloud Logging holds entries to order them. Below
// about a second, stderr arrives before the stdout that preceded it.
const bufferWindow = "1s"

// -------------------------------------------------------------------------
// STREAMING
// -------------------------------------------------------------------------

// StreamLogs writes a running execution's output to w as it arrives. A tail
// starts at what is being logged now, so it cannot resume from an offset.
func (p *Provider) StreamLogs(
	ctx context.Context, id execution.ID, offset int64, w io.Writer,
) error {
	if offset > 0 {
		return plugin.ErrNoResume
	}

	resp, err := p.openTail(ctx, jobName(id))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return copyTail(resp.Body, w)
}

// openTail starts the stream. Not routed through call, which reads the whole
// body, because this response never ends.
func (p *Provider) openTail(ctx context.Context, job string) (*http.Response, error) {
	// A bare object is rejected as "Invalid value (Object)"
	payload := []map[string]any{{
		"resourceNames": []string{"projects/" + p.cfg.Project},
		"filter": fmt.Sprintf(
			`resource.type="cloud_run_job" resource.labels.job_name=%q`, job),
		"bufferWindow": bufferWindow,
	}}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, plugin.Internal(fmt.Errorf("encoding the tail request: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.logsURL+"/v2/entries:tail", bytes.NewReader(encoded))
	if err != nil {
		return nil, plugin.Internal(fmt.Errorf("building the tail request: %w", err))
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Load().Do(req)
	if err != nil {
		return nil, plugin.Infrastructure(fmt.Errorf("opening the log stream: %w", err))
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxLogBytes))

		return nil, plugin.ClassifyHTTP(resp.StatusCode, plugin.RetryAfter(resp),
			fmt.Errorf("tail: %s", googleError(body)))
	}

	return resp, nil
}

// -------------------------------------------------------------------------
// DECODING
// -------------------------------------------------------------------------

// copyTail decodes the open array, writing each entry's text as it arrives. A
// read failure ends the copy quietly: no way a stream stops fails the work.
func copyTail(body io.Reader, w io.Writer) error {
	dec := json.NewDecoder(body)

	// The opening bracket of the response array.
	if _, err := dec.Token(); err != nil {
		return nil
	}

	for dec.More() {
		var chunk tailChunk

		if err := dec.Decode(&chunk); err != nil {
			return nil
		}

		if err := writeChunk(w, &chunk); err != nil {
			return err
		}
	}

	return nil
}

// tailChunk is one TailLogEntriesResponse off the stream.
type tailChunk struct {
	Entries []struct {
		TextPayload string `json:"textPayload"`
	} `json:"entries"`
}

// writeChunk emits a chunk's text, skipping the structural entries Cloud
// Logging interleaves with the container's own output.
func writeChunk(w io.Writer, chunk *tailChunk) error {
	for _, entry := range chunk.Entries {
		if entry.TextPayload == "" {
			continue
		}

		if _, err := io.WriteString(w, entry.TextPayload+"\n"); err != nil {
			return plugin.Internal(fmt.Errorf("writing log output: %w", err))
		}
	}

	return nil
}
