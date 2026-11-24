// -------------------------------------------------------------------------------
// Container Logs
//
// Author: Alex Freidah
//
// The task container's log through the Kubernetes API, followed while the task
// runs and read whole once it has ended when the follow did not see it exit.
// The log starts at the container's first line rather than at the present, so
// a capture resumes by skipping what the execution's log already holds.
// -------------------------------------------------------------------------------

package ibm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// defaultPodPoll is how often a capture checks whether the task's container
// has started.
const defaultPodPoll = 2 * time.Second

// -------------------------------------------------------------------------
// CAPTURE
// -------------------------------------------------------------------------

// CaptureLogs copies the task container's log into log as it is written, from
// log.Len() on. Before the workload has ended, the log is followed until the
// container exits; a follow that ends while it still runs was cut, and is
// reported for the capture to be made again. Once ended has closed, a log the
// follow did not see to the container's exit is read whole, so output a cut
// follow missed is not lost.
func (p *Provider) CaptureLogs(ctx context.Context, id execution.ID, log execution.Log, ended <-chan struct{}) error {
	run := plugin.ResourceName(id)

	if !plugin.Ended(ended) {
		exited, err := p.follow(ctx, run, log, ended)
		if err != nil {
			return err
		}

		select {
		case <-ended:
		case <-ctx.Done():
			return ctx.Err()
		}

		if exited {
			return nil
		}
	}

	return p.readRest(ctx, run, log)
}

// follow copies the container's log into log as it is written, once the
// container has started, and reports whether the container had exited when
// the log ended. Nothing is followed when ended closes first.
func (p *Provider) follow(ctx context.Context, run string, log execution.Log, ended <-chan struct{}) (bool, error) {
	name, err := p.awaitStart(ctx, run, ended)
	if err != nil || name == "" {
		return false, err
	}

	resp, err := p.openLog(ctx, name, true)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if err := plugin.CopyAll(plugin.SkipWriter(log, log.Len()), resp.Body); err != nil {
		return false, err
	}

	// A follow ends when the container exits, or when something on the way cuts it
	found, err := p.runPod(ctx, run)
	if err != nil {
		return false, err
	}

	if found != nil && found.started() && found.exitCode() == nil {
		return false, plugin.Infrastructure(fmt.Errorf("the log of job run %s ended while its container runs", run))
	}

	return found != nil && found.exitCode() != nil, nil
}

// awaitStart polls for the run's pod until its task container has started,
// returning the pod's name, or "" once ended closes first.
func (p *Provider) awaitStart(ctx context.Context, run string, ended <-chan struct{}) (string, error) {
	for {
		found, err := p.runPod(ctx, run)
		if err != nil {
			return "", err
		}

		if found != nil && found.started() {
			return found.Metadata.Name, nil
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ended:
			return "", nil
		case <-time.After(p.podPoll):
		}
	}
}

// readRest copies what of a finished run's container log log does not hold
// yet. The pod goes with the run, so a pod already gone is output lost.
func (p *Provider) readRest(ctx context.Context, run string, log execution.Log) error {
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

	return plugin.CopyAll(plugin.SkipWriter(log, log.Len()), resp.Body)
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
