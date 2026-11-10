// -------------------------------------------------------------------------------
// Code Engine Kubernetes API
//
// Author: Alex Freidah
//
// Where a job run's exit code and output live. The v2 API reports neither, so
// both come from the run's pod through the project's Kubernetes API, which
// takes the same IAM token. The one thing that API needs and the v2 API cannot
// give is the project's namespace, read once from the kubeconfig.
// -------------------------------------------------------------------------------

package ibm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// How a job run's pod is found, and which of its containers is the task.
const (
	jobRunLabel   = "codeengine.cloud.ibm.com/job-run"
	userContainer = "user-container"
)

// maxLogBytes bounds the output Result returns, the same bound Cloud Run uses.
const maxLogBytes = 256 << 10

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// pod is the part of a Kubernetes pod the plugin reads.
type pod struct {
	Metadata struct {
		Name              string    `json:"name"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Name  string `json:"name"`
			State struct {
				Running    *struct{} `json:"running"`
				Terminated *struct {
					ExitCode int    `json:"exitCode"`
					Reason   string `json:"reason"`
				} `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// exitCode is what the task's container exited with, nil until it has.
func (pd *pod) exitCode() *int {
	for _, cs := range pd.Status.ContainerStatuses {
		if cs.Name == userContainer && cs.State.Terminated != nil {
			return new(cs.State.Terminated.ExitCode)
		}
	}

	return nil
}

// started reports whether the task's container has begun, running or already
// finished, which is when its log can be opened.
func (pd *pod) started() bool {
	for _, cs := range pd.Status.ContainerStatuses {
		if cs.Name == userContainer {
			return cs.State.Running != nil || cs.State.Terminated != nil
		}
	}

	return false
}

// -------------------------------------------------------------------------
// ENDPOINTS
// -------------------------------------------------------------------------

// kubeBase is the region's Kubernetes API host.
func kubeBase(region string) string {
	return "https://proxy." + region + ".codeengine.cloud.ibm.com"
}

// podsURL is the namespace's pods collection.
func (p *Provider) podsURL(namespace string) string {
	return p.kubeURL + "/api/v1/namespaces/" + namespace + "/pods"
}

// -------------------------------------------------------------------------
// NAMESPACE
// -------------------------------------------------------------------------

// namespace returns the project's Kubernetes namespace, read once from the
// kubeconfig, the one call that takes a delegated refresh token. A failed read
// is not cached, so the next call tries again.
func (p *Provider) namespace(ctx context.Context) (string, error) {
	if ns := p.ns.Load(); ns != nil {
		return *ns, nil
	}

	drt, err := p.auth.Load().iam.delegated(ctx)
	if err != nil {
		return "", err
	}

	body, err := p.fetch(ctx, http.MethodGet, p.cfg.kubeconfigURL(p.apiURL),
		http.Header{"X-Delegated-Refresh-Token": {drt}}, nil)
	if err != nil {
		return "", err
	}

	var config struct {
		CurrentContext string `json:"current-context"`
		Contexts       []struct {
			Name    string `json:"name"`
			Context struct {
				Namespace string `json:"namespace"`
			} `json:"context"`
		} `json:"contexts"`
	}

	if err := json.Unmarshal(body, &config); err != nil {
		return "", plugin.Internal(fmt.Errorf("decoding the kubeconfig: %w", err))
	}

	for _, c := range config.Contexts {
		if c.Name == config.CurrentContext && c.Context.Namespace != "" {
			ns := c.Context.Namespace
			p.ns.Store(&ns)

			return ns, nil
		}
	}

	return "", plugin.Internal(fmt.Errorf("the kubeconfig for project %s names no namespace",
		p.cfg.ProjectID))
}

// -------------------------------------------------------------------------
// PODS
// -------------------------------------------------------------------------

// runPod finds a job run's pod by label: nil when there is none, before the run
// starts or after it is deleted. With retries off there is one; if there are
// ever more, the newest is the one that ran last.
func (p *Provider) runPod(ctx context.Context, run string) (*pod, error) {
	ns, err := p.namespace(ctx)
	if err != nil {
		return nil, err
	}

	var list struct {
		Items []pod `json:"items"`
	}

	query := url.Values{"labelSelector": {jobRunLabel + "=" + run}}
	if err := p.call(ctx, http.MethodGet, p.podsURL(ns)+"?"+query.Encode(), nil, &list); err != nil {
		return nil, err
	}

	if len(list.Items) == 0 {
		return nil, nil
	}

	newest := &list.Items[0]
	for i := range list.Items {
		if list.Items[i].Metadata.CreationTimestamp.After(newest.Metadata.CreationTimestamp) {
			newest = &list.Items[i]
		}
	}

	return newest, nil
}

// podLogs reads the task container's output from the start, up to maxLogBytes,
// reporting whether there was more.
func (p *Provider) podLogs(ctx context.Context, name string) ([]byte, bool, error) {
	ns, err := p.namespace(ctx)
	if err != nil {
		return nil, false, err
	}

	// One byte past the bound, so a log of exactly maxLogBytes is not called truncated
	query := url.Values{
		"container":  {userContainer},
		"limitBytes": {strconv.Itoa(maxLogBytes + 1)},
	}

	body, err := p.fetch(ctx, http.MethodGet, p.podsURL(ns)+"/"+name+"/log?"+query.Encode(),
		http.Header{"Accept": {"text/plain, */*"}}, nil)
	if err != nil {
		return nil, false, err
	}

	if len(body) > maxLogBytes {
		return body[:maxLogBytes], true, nil
	}

	return body, false, nil
}
