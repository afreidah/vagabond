// -------------------------------------------------------------------------------
// Container Logs
//
// Author: Alex Freidah
//
// The task container's log through the Kubernetes API, followed while the task
// runs and read whole once it has ended. The log starts at the container's
// first line rather than at the present, so unlike a tail a follow can resume
// from an offset by skipping what the caller already has.
// -------------------------------------------------------------------------------

package ibm

import (
	"context"
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

	resp, err := p.openLog(ctx, name, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return plugin.CopyLive(plugin.SkipWriter(w, offset), resp.Body)
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

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// ReadLogs writes a finished run's output, the task container's whole log. The
// pod goes with the run, so a pod already gone is output lost.
func (p *Provider) ReadLogs(ctx context.Context, id execution.ID, w io.Writer) error {
	run := runName(id)

	found, err := p.runPod(ctx, run)
	if err != nil {
		return err
	}

	if found == nil {
		return plugin.Infrastructure(fmt.Errorf("the pod of job run %s is gone, and its log with it", run))
	}

	// A container that never started printed nothing
	if !found.started() {
		return nil
	}

	resp, err := p.openLog(ctx, found.Metadata.Name, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return plugin.CopyAll(w, resp.Body)
}

// -------------------------------------------------------------------------
// OPENING
// -------------------------------------------------------------------------

// openLog opens the task container's log as a stream from its first line. A
// followed log stays open while the task runs.
func (p *Provider) openLog(ctx context.Context, podName string, follow bool) (*http.Response, error) {
	ns, err := p.namespace(ctx)
	if err != nil {
		return nil, err
	}

	query := url.Values{"container": {userContainer}}
	if follow {
		query.Set("follow", "true")
	}

	return p.open(ctx, http.MethodGet, p.podsURL(ns)+"/"+podName+"/log?"+query.Encode(),
		http.Header{"Accept": {"text/plain, */*"}}, nil)
}
