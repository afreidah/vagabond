//go:build containerd

// -------------------------------------------------------------------------------
// containerd Executor Tests
//
// Author: Alex Freidah
//
// Real workloads on the containerd the executor test environment starts, found
// by VAGABOND_CONTAINERD, in a namespace of their own. Needs root, so it is
// behind the containerd build tag and skips without the environment; make
// executor-test starts it, builds the tests as the user and runs them with
// sudo.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/defaults"
	"github.com/containerd/errdefs"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// newTestExecutor connects to the test environment's containerd, or skips
// when there is none. Containers get networks of their own when the
// environment names the CNI plugins, and use the node's otherwise.
func newTestExecutor(t *testing.T) *Containerd {
	t.Helper()

	socket := os.Getenv("VAGABOND_CONTAINERD")
	if socket == "" {
		t.Skip("VAGABOND_CONTAINERD names no containerd; make executor-test starts one")
	}

	plugins := os.Getenv("VAGABOND_CNI")

	c, err := New(&Config{
		Socket:    socket,
		Namespace: "vagabond-test",
		DataDir:   t.TempDir(),
		Cgroup:    "/vagabond-test",
		Network:   NetworkConfig{Host: plugins == "", CNIPath: plugins},
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.Info(t.Context()); err != nil {
		t.Fatalf("Info() = %v", err)
	}

	return c
}

// networked is the test executor with a network of its own for each
// container, or skips without the CNI plugins that needs.
func networked(t *testing.T) *Containerd {
	t.Helper()

	if os.Getenv("VAGABOND_CNI") == "" {
		t.Skip("VAGABOND_CNI names no CNI plugins; make executor-test fetches them")
	}

	return newTestExecutor(t)
}

// run submits a shell script as a workload, released when the test ends.
func run(t *testing.T, c *Containerd, id, script string, timeout time.Duration) {
	t.Helper()

	if err := c.Submit(t.Context(), id, workload(timeout, "sh", "-c", script)); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	t.Cleanup(func() { _ = c.Release(context.Background(), id) })
}

// finished polls a workload until it reaches a terminal state.
func finished(t *testing.T, c *Containerd, id string) execution.Status {
	t.Helper()

	deadline := time.Now().Add(time.Minute)

	for time.Now().Before(deadline) {
		st, err := c.status(t.Context(), id)
		if err != nil {
			t.Fatalf("status() = %v", err)
		}

		if st.State.Terminal() {
			return st
		}

		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("workload %s did not finish", id)

	return execution.Status{}
}

// heldAs is the workload id among the held ones, or nil.
func heldAs(t *testing.T, c *Containerd, id string) *agentrpc.Held {
	t.Helper()

	held, err := c.Held(t.Context())
	if err != nil {
		t.Fatalf("Held() = %v", err)
	}

	for _, h := range held {
		if h.GetExecutionId() == id {
			return h
		}
	}

	return nil
}

// gatedWriter collects what is written to it, holding its first write until
// open is closed and announcing it by closing first.
type gatedWriter struct {
	first chan struct{}
	open  chan struct{}
	once  sync.Once
	buf   strings.Builder
}

// Write blocks the first time until the gate opens, then collects p.
func (g *gatedWriter) Write(p []byte) (int, error) {
	g.once.Do(func() {
		close(g.first)
		<-g.open
	})

	return g.buf.Write(p)
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// A workload printing past the cap keeps only the end: a read from its first
// byte is placed where the kept part begins, and the front is freed on disk.
func TestContainerd_KeepsTheEndOfLongOutput(t *testing.T) {
	cfg := newTestExecutor(t).cfg
	cfg.MaxLogBytes = 4 * execution.LogChunk

	c, err := New(&cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	id := "exec-long-" + time.Now().Format("150405.000")
	printed := 6 * execution.LogChunk

	run(t, c, id, fmt.Sprintf("yes x | head -c %d; echo the end", printed), 0)
	finished(t, c, id)

	var logs strings.Builder

	w := &execution.PlacedWriter{Writer: &logs}

	if err := c.Logs(t.Context(), id, 0, w); err != nil || !strings.HasSuffix(logs.String(), "x\nthe end\n") {
		t.Fatalf("Logs() = %d bytes, %v; want the end", logs.Len(), err)
	}

	size := int64(printed + len("the end\n"))
	if start := execution.KeepFrom(size, cfg.MaxLogBytes) * execution.LogChunk; w.Start != start || int64(logs.Len()) != size-start {
		t.Errorf("Logs() placed at %d with %d bytes, want at %d with %d", w.Start, logs.Len(), start, size-start)
	}

	// The front is freed by the next pass of the trimming loop
	for deadline := time.Now().Add(2 * logTrim); allocated(t, c.logPath(id)) >= size; {
		if time.Now().After(deadline) {
			t.Fatalf("the front of %s was not freed", c.logPath(id))
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// A workload runs, exits with its own code, and its output and duration come
// back; released, it is gone.
func TestContainerd_RunsAWorkload(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-exit-" + time.Now().Format("150405.000")

	run(t, c, id, "echo hello from vagabond; exit 3", 0)

	if st := finished(t, c, id); st.State != execution.StateFailed || st.EndedAt.IsZero() {
		t.Errorf("status = %+v, want failed with an end time", st)
	}

	res, err := c.Result(t.Context(), id)
	if err != nil || res.ExitCode == nil || res.GetExitCode() != 3 {
		t.Fatalf("Result() = %+v, %v; want exit 3", res, err)
	}

	var logs strings.Builder

	if err := c.Logs(t.Context(), id, 0, &execution.PlacedWriter{Writer: &logs}); err != nil || !strings.Contains(logs.String(), "hello from vagabond") {
		t.Errorf("Logs() = %q, %v", logs.String(), err)
	}

	h := heldAs(t, c, id)
	if h.GetRunning() || h.GetResources().GetCpu() != 250 || h.GetResources().GetMemory() != 128 {
		t.Errorf("held = %+v, want finished with 250 millicores and 128 MiB", h)
	}

	if err := c.Release(t.Context(), id); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	if _, err := c.Status(t.Context(), id); !errors.Is(err, ErrUnknown) {
		t.Errorf("Status() after release = %v, want ErrUnknown", err)
	}
}

// An image reference that does not parse is refused as invalid, before any
// pull.
func TestContainerd_RefusesABadImage(t *testing.T) {
	c := newTestExecutor(t)
	w := workload(0, "true")
	w.Image = "Not A Reference"

	if err := c.Submit(t.Context(), "exec-bad-image", w); !errors.Is(err, ErrInvalid) {
		t.Errorf("Submit() = %v, want ErrInvalid", err)
	}
}

// A workload that cannot start leaves nothing behind, so a retry is not taken
// for a workload already there.
func TestContainerd_AFailedStartLeavesNothing(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-no-start-" + time.Now().Format("150405.000")

	if err := c.Submit(t.Context(), id, workload(0, "/no/such/program")); err == nil {
		_ = c.Release(context.Background(), id)
		t.Fatal("Submit() = nil, want the start to fail")
	}

	if _, err := c.Status(t.Context(), id); !errors.Is(err, ErrUnknown) {
		t.Errorf("Status() after a failed start = %v, want ErrUnknown", err)
	}
}

// Logs of a workload another executor holds is unknown here, with nothing
// written, so the caller can ask the next one.
func TestContainerd_LogsOfAnUnknownWorkload(t *testing.T) {
	c := newTestExecutor(t)

	var logs strings.Builder

	if err := c.Logs(t.Context(), "exec-elsewhere", 0, &execution.PlacedWriter{Writer: &logs}); !errors.Is(err, ErrUnknown) || logs.Len() > 0 {
		t.Errorf("Logs() = %q, %v; want nothing and ErrUnknown", logs.String(), err)
	}
}

// A workload whose task went after it started, as when the host restarts, has
// failed without an exit code and no longer takes room.
func TestContainerd_ATaskGoneAfterItStarted(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-gone-" + time.Now().Format("150405.000")

	run(t, c, id, "sleep 60", 0)

	_, task, err := c.load(t.Context(), id)
	if err != nil {
		t.Fatalf("load() = %v", err)
	}

	if _, err := task.Delete(t.Context(), client.WithProcessKill); err != nil {
		t.Fatalf("deleting the task: %v", err)
	}

	st, err := c.Status(t.Context(), id)
	if err != nil || st.GetState() != string(execution.StateFailed) {
		t.Errorf("Status() = %+v, %v; want failed", st, err)
	}

	if res, err := c.Result(t.Context(), id); err != nil || res.ExitCode != nil {
		t.Errorf("Result() = %+v, %v; want no exit code", res, err)
	}

	if h := heldAs(t, c, id); h.GetRunning() {
		t.Errorf("held = %+v, want not running", h)
	}
}

// Output read from an offset starts after the bytes the caller already has.
func TestContainerd_LogsFromAnOffset(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-offset-" + time.Now().Format("150405.000")

	run(t, c, id, "echo hello from vagabond", 0)
	finished(t, c, id)

	var rest strings.Builder

	if err := c.Logs(t.Context(), id, 6, &execution.PlacedWriter{Writer: &rest}); err != nil || rest.String() != "from vagabond\n" {
		t.Errorf("Logs() from offset 6 = %q, %v; want the rest of the line", rest.String(), err)
	}
}

// A follower whose workload is released mid-follow, as the dispatcher releases
// one once it has the result, still gets every line and ends without an error.
func TestContainerd_LogsFollowsThroughRelease(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-logs-" + time.Now().Format("150405.000")

	run(t, c, id, "echo first; sleep 1; echo last", 0)

	w := &gatedWriter{first: make(chan struct{}), open: make(chan struct{})}
	done := make(chan error, 1)

	go func() { done <- c.Logs(t.Context(), id, 0, &execution.PlacedWriter{Writer: w}) }()

	select {
	case <-w.first:
	case err := <-done:
		t.Fatalf("Logs() = %v before any output", err)
	case <-time.After(time.Minute):
		t.Fatal("Logs() wrote nothing")
	}

	// The follower waits in its first write while the workload ends and goes.
	finished(t, c, id)

	if err := c.Release(t.Context(), id); err != nil {
		t.Fatalf("Release() = %v", err)
	}

	close(w.open)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Logs() = %v, want nil after a release", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("Logs() did not end after the release")
	}

	if got := w.buf.String(); got != "first\nlast\n" {
		t.Errorf("logs = %q, want both lines", got)
	}
}

// A workload outlives the executor that started it: a second executor on the
// same namespace and data directory, as a restarted agent would open, finds it,
// watches it, and collects its result.
func TestContainerd_SurvivesARestart(t *testing.T) {
	first := newTestExecutor(t)
	id := "exec-restart-" + time.Now().Format("150405.000")

	run(t, first, id, "sleep 2; echo after the restart; exit 5", 0)

	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second, err := New(&first.cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	t.Cleanup(func() { _ = second.Close() })
	t.Cleanup(func() { _ = second.Release(context.Background(), id) })

	if err := second.Recover(t.Context()); err != nil {
		t.Fatalf("Recover() = %v", err)
	}

	finished(t, second, id)

	res, err := second.Result(t.Context(), id)
	if err != nil || res.ExitCode == nil || res.GetExitCode() != 5 {
		t.Errorf("Result() = %+v, %v; want exit 5", res, err)
	}

	var logs strings.Builder

	if err := second.Logs(t.Context(), id, 0, &execution.PlacedWriter{Writer: &logs}); err != nil || !strings.Contains(logs.String(), "after the restart") {
		t.Errorf("Logs() = %q, %v; want the output from before the restart", logs.String(), err)
	}
}

// A cancelled workload stops and reports cancelled.
func TestContainerd_Cancel(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-cancel-" + time.Now().Format("150405.000")

	run(t, c, id, "sleep 60", 0)

	if err := c.Cancel(t.Context(), id); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}

	if st := finished(t, c, id); st.State != execution.StateCancelled {
		t.Errorf("status = %s, want cancelled", st.State)
	}
}

// A workload past its timeout is stopped.
func TestContainerd_Timeout(t *testing.T) {
	c := newTestExecutor(t)
	id := "exec-timeout-" + time.Now().Format("150405.000")

	run(t, c, id, "sleep 60", time.Second)

	if st := finished(t, c, id); st.State != execution.StateFailed {
		t.Errorf("status = %s, want failed after the timeout", st.State)
	}
}

// A workload reaches the internet but not the node, whose address is a
// private one.
func TestContainerd_IsolatesAWorkload(t *testing.T) {
	c := networked(t)
	id := "exec-isolated-" + time.Now().Format("150405.000")

	run(t, c, id, reachScript(t), 0)
	finished(t, c, id)

	if internet, node := reached(t, c.Logs, id); !internet || node {
		t.Errorf("reached the internet %t and the node %t, want only the internet", internet, node)
	}
}

// A workload whose task asks for the node's private networks reaches the node
// as well as the internet.
func TestContainerd_PrivateWorkloadReachesTheNode(t *testing.T) {
	c := networked(t)
	id := "exec-private-" + time.Now().Format("150405.000")

	w := workload(0, "sh", "-c", reachScript(t))
	w.PrivateNetwork = true

	if err := c.Submit(t.Context(), id, w); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	t.Cleanup(func() { _ = c.Release(context.Background(), id) })
	finished(t, c, id)

	if internet, node := reached(t, c.Logs, id); !internet || !node {
		t.Errorf("reached the internet %t and the node %t, want both", internet, node)
	}
}

// With -host-network a workload runs on the node's network and reaches
// everything the node does.
func TestContainerd_HostNetwork(t *testing.T) {
	cfg := newTestExecutor(t).cfg
	cfg.Network = NetworkConfig{Host: true}

	c, err := New(&cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	id := "exec-host-net-" + time.Now().Format("150405.000")

	run(t, c, id, reachScript(t), 0)
	finished(t, c, id)

	if internet, node := reached(t, c.Logs, id); !internet || !node {
		t.Errorf("reached the internet %t and the node %t, want both", internet, node)
	}
}

// An executor whose CNI plugins are missing refuses to start, naming the
// first one it looked for.
func TestContainerd_NewWithoutPlugins(t *testing.T) {
	cfg := newTestExecutor(t).cfg
	cfg.Network = NetworkConfig{CNIPath: t.TempDir()}

	if _, err := New(&cfg); err == nil || !strings.Contains(err.Error(), "the CNI plugin ptp") {
		t.Errorf("New() = %v, want the missing plugin named", err)
	}
}

// A workload whose network cannot be set up, attached or its rules written,
// fails its submission and leaves nothing behind for a retry to find.
func TestContainerd_AFailedNetworkLeavesNothing(t *testing.T) {
	cases := map[string]func(n *network){
		"attach": func(n *network) { n.cni = &fakeCNI{setupErr: errors.New("refused")} },
		"rules": func(n *network) {
			n.run = func(context.Context, string, string, ...string) error { return errors.New("refused") }
		},
	}

	for name, breaks := range cases {
		t.Run(name, func(t *testing.T) {
			c := networked(t)
			breaks(c.net)

			id := "exec-no-net-" + name + "-" + time.Now().Format("150405.000")

			if err := c.Submit(t.Context(), id, workload(0, "true")); err == nil || !strings.Contains(err.Error(), "refused") {
				_ = c.Release(context.Background(), id)
				t.Fatalf("Submit() = %v, want the network's failure", err)
			}

			if _, err := c.Status(t.Context(), id); !errors.Is(err, ErrUnknown) {
				t.Errorf("Status() after a failed network = %v, want ErrUnknown", err)
			}
		})
	}
}

// A workload whose address cannot be freed is kept, so a later Release can
// try again rather than leave the address held.
func TestContainerd_ReleaseKeepsWhatItCannotFree(t *testing.T) {
	c := networked(t)
	id := "exec-keep-addr-" + time.Now().Format("150405.000")

	run(t, c, id, "true", 0)
	finished(t, c, id)

	attached := c.net.cni
	c.net.cni = &fakeCNI{removeErr: errors.New("refused")}

	if err := c.Release(t.Context(), id); err == nil || !strings.Contains(err.Error(), "freeing the address") {
		t.Errorf("Release() = %v, want the address's failure", err)
	}

	if _, err := c.Status(t.Context(), id); err != nil {
		t.Errorf("Status() after a failed release = %v, want the workload still held", err)
	}

	c.net.cni = attached
}

// -------------------------------------------------------------------------
// HARDENING
// -------------------------------------------------------------------------

// statusScript prints what a workload runs under: its NoNewPrivs, Seccomp and
// CapEff lines, its user ID mapping, and "mknod" if it could make a device
// node.
const statusScript = `grep -E '^(NoNewPrivs|Seccomp|CapEff):' /proc/self/status
echo "uid_map $(cat /proc/self/uid_map)"
mknod /tmp/device c 1 3 2>/dev/null && echo mknod
true`

// runWith runs script on an executor made from the test one's config as change
// leaves it, and returns the workload's output.
func runWith(t *testing.T, change func(cfg *Config), script string) string {
	t.Helper()

	cfg := newTestExecutor(t).cfg
	change(&cfg)

	c, err := New(&cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	id := "exec-hardened-" + time.Now().Format("150405.000")

	run(t, c, id, script, 0)
	finished(t, c, id)

	return outputOf(t, c.Logs, id)
}

// field is what follows name on the line of out it begins, its colon aside.
func field(out, name string) string {
	for line := range strings.Lines(out) {
		if fields := strings.Fields(line); len(fields) > 1 && strings.TrimSuffix(fields[0], ":") == name {
			return strings.Join(fields[1:], " ")
		}
	}

	return ""
}

// A workload gains no privileges, keeps only the default capabilities, runs
// under the default seccomp profile, cannot make device nodes, and its root
// is a user in one of the node's ID ranges, not the node's root.
func TestContainerd_HardensAWorkload(t *testing.T) {
	out := runWith(t, func(*Config) {}, statusScript)

	mapping := strings.Fields(field(out, "uid_map"))
	host := 0

	if len(mapping) == 3 {
		host, _ = strconv.Atoi(mapping[1])
	}

	switch {
	case field(out, "NoNewPrivs") != "1":
		t.Errorf("NoNewPrivs = %q, want 1 in:\n%s", field(out, "NoNewPrivs"), out)
	case field(out, "Seccomp") != "2":
		t.Errorf("Seccomp = %q, want 2, a filter, in:\n%s", field(out, "Seccomp"), out)
	case field(out, "CapEff") != "00000000000004fb":
		t.Errorf("CapEff = %q, want the eight default capabilities in:\n%s", field(out, "CapEff"), out)
	case strings.Contains(out, "mknod"):
		t.Errorf("made a device node:\n%s", out)
	case len(mapping) != 3 || mapping[0] != "0" || mapping[2] != "65536" || host < firstHostID:
		t.Errorf("uid_map = %v, want 65536 IDs from a host range at or above %d", mapping, firstHostID)
	}
}

// -cap-add gives a workload a capability beyond the defaults.
func TestContainerd_CapAdd(t *testing.T) {
	out := runWith(t, func(cfg *Config) { cfg.CapAdd = []string{"NET_RAW"} }, statusScript)

	if got := field(out, "CapEff"); got != "00000000000024fb" {
		t.Errorf("CapEff = %q, want the defaults and CAP_NET_RAW", got)
	}
}

// With -no-userns a workload's root is the node's root.
func TestContainerd_NoUserNS(t *testing.T) {
	out := runWith(t, func(cfg *Config) { cfg.NoUserNS = true }, statusScript)

	if got := field(out, "uid_map"); got != "0 0 4294967295" {
		t.Errorf("uid_map = %q, want the node's own IDs", got)
	}
}

// With -read-only-root a workload can write /tmp but not its root.
func TestContainerd_ReadOnlyRoot(t *testing.T) {
	out := runWith(t, func(cfg *Config) { cfg.ReadOnlyRoot = true },
		"touch /written 2>/dev/null && echo root; touch /tmp/written && echo tmp; true")

	if !strings.Contains(out, "tmp") || strings.Contains(out, "root") {
		t.Errorf("output %q, want /tmp written and the root not", out)
	}
}

// Two workloads running at once each get an ID range of their own.
func TestContainerd_EachWorkloadItsOwnIDRange(t *testing.T) {
	c := newTestExecutor(t)
	ranges := make(map[int]bool)

	for i := range 2 {
		id := fmt.Sprintf("exec-range-%d-%s", i, time.Now().Format("150405.000"))
		run(t, c, id, "sleep 30", 0)

		container, err := c.client.LoadContainer(t.Context(), id)
		if err != nil {
			t.Fatalf("LoadContainer() = %v", err)
		}

		labels, err := labelsOf(t.Context(), container)
		if err != nil {
			t.Fatalf("labelsOf() = %v", err)
		}

		ranges[labels.idRange] = true
	}

	if len(ranges) != 2 || ranges[-1] {
		t.Errorf("ranges = %v, want two different ones", ranges)
	}
}

// Ranges reserved at once, before any container carries one as a label,
// still differ.
func TestContainerd_TakeRangeHoldsEachReservation(t *testing.T) {
	c := newTestExecutor(t)

	first, releaseFirst, err := c.takeRange(t.Context())
	if err != nil {
		t.Fatalf("takeRange() = %v", err)
	}

	defer releaseFirst()

	second, releaseSecond, err := c.takeRange(t.Context())
	if err != nil {
		t.Fatalf("takeRange() = %v", err)
	}

	defer releaseSecond()

	if first == second {
		t.Errorf("both reservations took range %d", first)
	}
}

// Info checks the snapshotter a namespace names as its default: it passes one
// that mounts images ID-mapped, and refuses, by name, one that cannot and one
// containerd does not have.
func TestContainerd_InfoNeedsIDMappedMounts(t *testing.T) {
	base := newTestExecutor(t).cfg

	for snapshotter, want := range map[string]string{
		"overlayfs": "",
		"devmapper": "devmapper snapshotter cannot mount images ID-mapped",
		"nosuch":    "reading what containerd's nosuch snapshotter can do",
	} {
		t.Run(snapshotter, func(t *testing.T) {
			_, err := defaultingTo(t, &base, snapshotter).Info(t.Context())
			if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
				t.Errorf("Info() = %v, want %q", err, want)
			}
		})
	}
}

// defaultingTo is an executor from base in a namespace of its own whose label
// makes snapshotter its default, so the label leaves the other tests'
// containers alone. The namespace goes when the test ends.
func defaultingTo(t *testing.T, base *Config, snapshotter string) *Containerd {
	t.Helper()

	cfg := *base
	cfg.Namespace = "vagabond-test-" + snapshotter

	c, err := New(&cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	namespaces := c.client.NamespaceService()
	if err := namespaces.Create(t.Context(), cfg.Namespace, nil); err != nil && !errdefs.IsAlreadyExists(err) {
		t.Fatalf("creating namespace %s: %v", cfg.Namespace, err)
	}

	t.Cleanup(func() { _ = namespaces.Delete(context.Background(), cfg.Namespace) })

	if err := namespaces.SetLabel(t.Context(), cfg.Namespace, defaults.DefaultSnapshotterNSLabel, snapshotter); err != nil {
		t.Fatalf("labelling namespace %s: %v", cfg.Namespace, err)
	}

	return c
}
