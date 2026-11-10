// -------------------------------------------------------------------------------
// Code Engine Kubernetes API Tests
//
// Author: Alex Freidah
//
// The namespace lookup, finding a run's pod, and reading its exit code and
// output, against the fake's Kubernetes routes.
// -------------------------------------------------------------------------------

package ibm

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// NAMESPACE
// -------------------------------------------------------------------------

// The namespace is read once, with the delegated refresh token, and kept.
func TestNamespace_ReadOnce(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	for range 3 {
		ns, err := p.namespace(t.Context())
		if err != nil || ns != testNamespace {
			t.Fatalf("namespace() = %q, %v; want %q", ns, err, testNamespace)
		}
	}

	calls := f.requestsTo("GET /api/v1/project/" + testProject + "/config")
	if len(calls) != 1 {
		t.Fatalf("%d kubeconfig reads, want 1", len(calls))
	}

	if calls[0].header.Get("X-Delegated-Refresh-Token") != "drt" || calls[0].header.Get("Authorization") != "Bearer tok" {
		t.Errorf("kubeconfig headers = %v", calls[0].header)
	}
}

// A failed read is not kept, so the next call tries again.
func TestNamespace_FailureNotCached(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET /api/v1/project/"+testProject+"/config",
		reply(http.StatusBadGateway, map[string]any{"code": "internal", "message": "An internal error occurred."}),
		reply(http.StatusOK, kubeconfig(testNamespace)))

	if _, err := p.namespace(t.Context()); err == nil {
		t.Fatal("namespace() survived a 502")
	}

	if ns, err := p.namespace(t.Context()); err != nil || ns != testNamespace {
		t.Fatalf("namespace() after a failure = %q, %v", ns, err)
	}
}

// A kubeconfig naming no namespace, or not decoding, is internal.
func TestNamespace_UnusableKubeconfig(t *testing.T) {
	t.Parallel()

	for name, handler := range map[string]http.HandlerFunc{
		"no namespace": reply(http.StatusOK, map[string]any{"current-context": "other", "contexts": []any{}}),
		"not json": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "apiVersion: v1")
		},
	} {
		f, p := newFakeIBM(t)
		f.on("GET /api/v1/project/"+testProject+"/config", handler)

		if _, err := p.namespace(t.Context()); err == nil {
			t.Errorf("%s: namespace() succeeded", name)
		}
	}
}

// -------------------------------------------------------------------------
// PODS
// -------------------------------------------------------------------------

// A run's pod is found by its label, and the newest is the one that ran last.
func TestRunPod_NewestByLabel(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	now := time.Now().Truncate(time.Second)

	f.on("GET "+podsPath(), reply(http.StatusOK, podList(
		podBody("run1-0-0", now.Add(-time.Minute), new(1)),
		podBody("run1-0-1", now, new(0)),
	)))

	found, err := p.runPod(t.Context(), "run1")
	if err != nil {
		t.Fatalf("runPod() = %v", err)
	}

	if found == nil || found.Metadata.Name != "run1-0-1" {
		t.Fatalf("runPod() = %+v, want run1-0-1", found)
	}

	selector := f.requestsTo("GET " + podsPath())[0].query.Get("labelSelector")
	if selector != jobRunLabel+"=run1" {
		t.Errorf("labelSelector = %q", selector)
	}
}

// No pod is nil, not an error: the run has not started, or is gone.
func TestRunPod_None(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)

	f.on("GET "+podsPath(), reply(http.StatusOK, podList()))

	if found, err := p.runPod(t.Context(), "run1"); err != nil || found != nil {
		t.Fatalf("runPod() = %+v, %v; want nil, nil", found, err)
	}
}

// The exit code and start come from the task's container, not a sidecar.
func TestPod_ReadsTheUserContainer(t *testing.T) {
	t.Parallel()

	var pd pod

	if pd.exitCode() != nil || pd.started() {
		t.Error("a pod with no containers reports an exit or a start")
	}

	sidecar := podBody("run1-0-0", time.Now(), new(9))
	statuses := sidecar["status"].(map[string]any)["containerStatuses"].([]map[string]any)
	statuses[0]["name"] = "istio-proxy"

	f, p := newFakeIBM(t)
	f.on("GET "+podsPath(), reply(http.StatusOK, podList(sidecar)))

	found, err := p.runPod(t.Context(), "run1")
	if err != nil {
		t.Fatalf("runPod() = %v", err)
	}

	if found.exitCode() != nil || found.started() {
		t.Error("a sidecar's state was read as the task's")
	}
}

// A running container has started and has no exit code yet.
func TestPod_Running(t *testing.T) {
	t.Parallel()

	f, p := newFakeIBM(t)
	f.on("GET "+podsPath(), reply(http.StatusOK, podList(podBody("run1-0-0", time.Now(), nil))))

	found, err := p.runPod(t.Context(), "run1")
	if err != nil {
		t.Fatalf("runPod() = %v", err)
	}

	if !found.started() || found.exitCode() != nil {
		t.Errorf("started %t, exit %v; want started with no exit", found.started(), found.exitCode())
	}
}

// -------------------------------------------------------------------------
// LOGS
// -------------------------------------------------------------------------

// The log is read from the task's container, bounded one byte past the limit
// so a log of exactly the limit is not called truncated.
func TestPodLogs_Bounds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		size      int
		truncated bool
	}{
		{100, false},
		{maxLogBytes, false},
		{maxLogBytes + 1, true},
	}

	for _, tc := range cases {
		f, p := newFakeIBM(t)

		f.on("GET "+podsPath()+"/run1-0-0/log", func(w http.ResponseWriter, r *http.Request) {
			limit, _ := strconv.Atoi(r.URL.Query().Get("limitBytes"))
			_, _ = io.WriteString(w, strings.Repeat("x", min(tc.size, limit)))
		})

		logs, truncated, err := p.podLogs(t.Context(), "run1-0-0")
		if err != nil {
			t.Fatalf("podLogs() = %v", err)
		}

		if len(logs) != min(tc.size, maxLogBytes) || truncated != tc.truncated {
			t.Errorf("size %d: read %d bytes, truncated %t", tc.size, len(logs), truncated)
		}

		got := f.requestsTo("GET " + podsPath() + "/run1-0-0/log")[0]
		if got.query.Get("container") != userContainer || got.query.Get("limitBytes") != strconv.Itoa(maxLogBytes+1) {
			t.Errorf("query = %v", got.query)
		}

		if !strings.HasPrefix(got.header.Get("Accept"), "text/plain") {
			t.Errorf("Accept = %q", got.header.Get("Accept"))
		}
	}
}
