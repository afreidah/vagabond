// -------------------------------------------------------------------------------
// Log Streaming
//
// Author: Alex Freidah
//
// entries:tail, which is a bidirectional streaming method reachable over plain
// HTTP through gRPC transcoding. Two details cost an attempt each to find: the
// request body is a JSON array because the body is a stream of requests, and
// the response is a JSON array that stays open rather than one object per line.
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

// bufferWindow is how long Cloud Logging holds entries to order them.
//
// Latency traded for correct ordering. Below about a second, stderr starts
// arriving before the stdout that preceded it.
const bufferWindow = "1s"

// StreamLogs writes a running execution's output to w as it arrives.
func (p *Provider) StreamLogs(
	ctx context.Context, id execution.ID, w io.Writer,
) error {
	resp, err := p.openTail(ctx, jobName(id))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return copyTail(resp.Body, w)
}

// openTail starts the stream.
//
// Not routed through call, which reads the whole body: the point here is a
// response that never ends.
func (p *Provider) openTail(ctx context.Context, job string) (*http.Response, error) {
	// An array, because a streaming method's body is a stream of requests. A
	// bare object is rejected as "Invalid value (Object)".
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

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, plugin.Infrastructure(fmt.Errorf("opening the log stream: %w", err))
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxLogBytes))

		return nil, plugin.ClassifyHTTP(resp.StatusCode, retryAfter(resp),
			fmt.Errorf("tail: %s", googleError(body)))
	}

	return resp, nil
}

// copyTail decodes the open array, writing each entry's text as it arrives.
//
// Decode blocks until a whole element has been received, which is what makes
// this a stream rather than a poll.
//
// A read failure ends the copy without reporting one. Once the stream is open,
// every way it stops is the same to the caller, and none is a reason to fail
// work the provider is still doing.
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
