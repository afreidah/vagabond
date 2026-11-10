// -------------------------------------------------------------------------------
// Log Streaming
//
// Author: Alex Freidah
//
// Follows the task container's log through the Kubernetes API. The stream
// starts at the container's first line rather than at the present, so unlike a
// tail it can resume from an offset by skipping what the caller already has.
// -------------------------------------------------------------------------------

package ibm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// defaultPodPoll is how often a follower checks whether the task's container
// has started.
const defaultPodPoll = 2 * time.Second

// -------------------------------------------------------------------------
// STREAMING
// -------------------------------------------------------------------------

// StreamLogs writes a running execution's output to w as it is written, from
// byte offset on. It waits for the container to start, since its log cannot be
// opened before, and returns when the container exits or ctx ends.
func (p *Provider) StreamLogs(ctx context.Context, id execution.ID, offset int64, w io.Writer) error {
	name, err := p.awaitStart(ctx, runName(id))
	if err != nil || name == "" {
		return err
	}

	resp, err := p.openFollow(ctx, name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return copyFollow(resp.Body, w, offset)
}

// awaitStart polls for the run's pod until its task container has started,
// returning the pod's name, or "" once ctx ends.
func (p *Provider) awaitStart(ctx context.Context, run string) (string, error) {
	for {
		found, err := p.runPod(ctx, run)
		if err != nil {
			if ctx.Err() != nil {
				return "", nil
			}

			return "", err
		}

		if found != nil && found.started() {
			return found.Metadata.Name, nil
		}

		select {
		case <-ctx.Done():
			return "", nil
		case <-time.After(p.podPoll):
		}
	}
}

// openFollow opens the container's log as a stream. Not routed through fetch,
// which reads the whole body, because this one stays open while the task runs.
func (p *Provider) openFollow(ctx context.Context, podName string) (*http.Response, error) {
	ns, err := p.namespace(ctx)
	if err != nil {
		return nil, err
	}

	query := url.Values{"follow": {"true"}, "container": {userContainer}}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.podsURL(ns)+"/"+podName+"/log?"+query.Encode(), nil)
	if err != nil {
		return nil, plugin.Internal(fmt.Errorf("building the log request: %w", err))
	}

	req.Header.Set("Accept", "text/plain, */*")

	resp, err := p.auth.Load().http.Do(req)
	if err != nil {
		if classified, ok := errors.AsType[*plugin.Error](err); ok {
			return nil, classified
		}

		return nil, plugin.Infrastructure(fmt.Errorf("opening the log stream: %w", err))
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))

		return nil, p.classify(resp, http.MethodGet, body)
	}

	return resp, nil
}

// -------------------------------------------------------------------------
// COPYING
// -------------------------------------------------------------------------

// copyFollow skips the offset bytes the caller already has, then copies the
// rest as it arrives. A read failure ends the copy quietly: no way a stream
// stops fails the work. A failed write is the caller's to know about.
func copyFollow(body io.Reader, w io.Writer, offset int64) error {
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, body, offset); err != nil {
			return nil
		}
	}

	buf := make([]byte, 32<<10)

	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return plugin.Internal(fmt.Errorf("writing log output: %w", err))
			}
		}

		if readErr != nil {
			return nil
		}
	}
}
