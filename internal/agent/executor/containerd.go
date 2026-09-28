// -------------------------------------------------------------------------------
// containerd Executor
//
// Author: Alex Freidah
//
// Runs workloads as containers on the node's containerd, in their own
// namespace. The container ID is the execution ID, output goes to a log file
// containerd writes itself, and everything the executor needs to know about a
// workload is on the container as labels, so an agent that restarts finds its
// workloads, their state, and their timeouts where it left them.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/afreidah/vagabond/internal/execution"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Labels a workload's container carries, so its state survives the agent.
const (
	labelStarted   = "vagabond.started"
	labelTimeout   = "vagabond.timeout"
	labelCancelled = "vagabond.cancelled"
	labelCPU       = "vagabond.cpu"
	labelMemory    = "vagabond.memory"
)

// Timings for stopping a workload, polling a log that is still being written,
// and reading a finished workload's output.
const (
	stopGrace    = 10 * time.Second
	logPoll      = 250 * time.Millisecond
	maxTailBytes = execution.MaxStoredLogs
)

// DefaultRuntime is containerd's runc shim.
const DefaultRuntime = "io.containerd.runc.v2"

// ErrUnknown reports an execution this executor holds no workload for.
var ErrUnknown = errors.New("no such workload")

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Spec is a workload to run: a task already translated to what a container
// needs. CPU is millicores, Memory MiB; zero leaves the image's defaults.
type Spec struct {
	Image      string
	Command    []string
	Args       []string
	Env        map[string]string
	WorkingDir string
	CPU        int64
	Memory     int64
	Timeout    time.Duration
	Runtime    string
}

// Held is one workload the executor holds: what it declared, in millicores and
// MiB, and whether it is still running and so still using that much.
type Held struct {
	ID      string
	CPU     int64
	Memory  int64
	Running bool
}

// Config is where the executor runs workloads. Cgroup is the parent every
// workload's cgroup is created under.
type Config struct {
	Socket    string
	Namespace string
	DataDir   string
	Cgroup    string
}

// Containerd runs workloads on one containerd.
type Containerd struct {
	client  *client.Client
	cfg     Config
	mu      sync.Mutex
	watched map[string]bool
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New connects to containerd and makes the log directory.
func New(cfg Config) (*Containerd, error) {
	c, err := client.New(cfg.Socket, client.WithDefaultNamespace(cfg.Namespace))
	if err != nil {
		return nil, fmt.Errorf("connecting to containerd at %s: %w", cfg.Socket, err)
	}

	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "logs"), 0o750); err != nil {
		_ = c.Close()

		return nil, fmt.Errorf("creating the log directory: %w", err)
	}

	return &Containerd{client: c, cfg: cfg, watched: make(map[string]bool)}, nil
}

// Close disconnects from containerd. Workloads keep running.
func (c *Containerd) Close() error {
	return c.client.Close()
}

// Check reports whether containerd answers.
func (c *Containerd) Check(ctx context.Context) error {
	if _, err := c.client.Version(ctx); err != nil {
		return fmt.Errorf("containerd at %s does not answer: %w", c.cfg.Socket, err)
	}

	return nil
}

// Runtimes lists the runtimes workloads can ask for.
func (c *Containerd) Runtimes() []string {
	return []string{DefaultRuntime}
}

// -------------------------------------------------------------------------
// RUNNING
// -------------------------------------------------------------------------

// Submit pulls the image and starts the workload under id. A workload already
// under id is left as it is, since the ID is the idempotency key.
func (c *Containerd) Submit(ctx context.Context, id string, spec *Spec) error {
	if _, err := c.client.LoadContainer(ctx, id); err == nil {
		return nil
	}

	ref, err := reference.ParseDockerRef(spec.Image)
	if err != nil {
		return fmt.Errorf("image %q: %w", spec.Image, err)
	}

	image, err := c.client.Pull(ctx, ref.String(), client.WithPullUnpack)
	if err != nil {
		return fmt.Errorf("pulling %s: %w", ref, err)
	}

	runtime := spec.Runtime
	if runtime == "" {
		runtime = DefaultRuntime
	}

	container, err := c.client.NewContainer(ctx, id,
		client.WithNewSnapshot(id, image),
		client.WithNewSpec(c.specOpts(id, image, spec)...),
		client.WithRuntime(runtime, nil),
		client.WithContainerLabels(map[string]string{
			labelTimeout: spec.Timeout.String(),
			labelCPU:     strconv.FormatInt(spec.CPU, 10),
			labelMemory:  strconv.FormatInt(spec.Memory, 10),
		}),
	)
	if err != nil {
		return fmt.Errorf("creating the container: %w", err)
	}

	task, err := container.NewTask(ctx, cio.LogFile(c.logPath(id)))
	if err != nil {
		_ = container.Delete(ctx, client.WithSnapshotCleanup)

		return fmt.Errorf("creating the task: %w", err)
	}

	if _, err := container.SetLabels(ctx, map[string]string{labelStarted: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		return fmt.Errorf("labelling the container: %w", err)
	}

	if err := task.Start(ctx); err != nil {
		return fmt.Errorf("starting the task: %w", err)
	}

	c.watch(id, spec.Timeout, time.Now())

	return nil
}

// specOpts translates a spec into the container's OCI spec: the image's
// defaults, then the spec's overrides, its limits under the executor's
// cgroup, and the host's network.
func (c *Containerd) specOpts(id string, image client.Image, spec *Spec) []oci.SpecOpts {
	opts := []oci.SpecOpts{oci.WithImageConfig(image)}

	switch {
	case len(spec.Command) > 0:
		opts = append(opts, oci.WithProcessArgs(append(spec.Command, spec.Args...)...))
	case len(spec.Args) > 0:
		opts = append(opts, oci.WithImageConfigArgs(image, spec.Args))
	}

	if len(spec.Env) > 0 {
		env := make([]string, 0, len(spec.Env))
		for k, v := range spec.Env {
			env = append(env, k+"="+v)
		}

		opts = append(opts, oci.WithEnv(env))
	}

	if spec.WorkingDir != "" {
		opts = append(opts, oci.WithProcessCwd(spec.WorkingDir))
	}

	if spec.Memory > 0 {
		opts = append(opts, oci.WithMemoryLimit(uint64(spec.Memory)<<20)) //nolint:gosec // positive
	}

	if spec.CPU > 0 {
		opts = append(opts, oci.WithCPUCFS(spec.CPU*100, 100000))
	}

	return append(opts,
		oci.WithCgroup(filepath.Join(c.cfg.Cgroup, id)),
		oci.WithHostNamespace(specs.NetworkNamespace),
		oci.WithHostResolvconf,
		oci.WithHostHostsFile,
	)
}

// Cancel stops a workload: a SIGTERM, then a SIGKILL after stopGrace if it has
// not exited. Cancelling one that already ended is not an error.
func (c *Containerd) Cancel(ctx context.Context, id string) error {
	container, task, err := c.load(ctx, id)
	if err != nil {
		return err
	}

	if _, err := container.SetLabels(ctx, map[string]string{labelCancelled: "true"}); err != nil {
		return fmt.Errorf("labelling the container: %w", err)
	}

	if task == nil {
		return nil
	}

	return c.stop(ctx, task)
}

// stop sends SIGTERM and, from its own context, SIGKILL after the grace
// period.
func (c *Containerd) stop(ctx context.Context, task client.Task) error {
	exited, err := task.Wait(context.WithoutCancel(ctx))
	if err != nil {
		return fmt.Errorf("waiting on the task: %w", err)
	}

	if err := task.Kill(ctx, syscall.SIGTERM); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("stopping the task: %w", err)
	}

	go func() {
		select {
		case <-exited:
		case <-time.After(stopGrace):
			_ = task.Kill(context.WithoutCancel(ctx), syscall.SIGKILL)
		}
	}()

	return nil
}

// Release deletes a finished workload: its task, container, snapshot and log.
func (c *Containerd) Release(ctx context.Context, id string) error {
	container, task, err := c.load(ctx, id)
	if errors.Is(err, ErrUnknown) {
		return nil
	}

	if err != nil {
		return err
	}

	if task != nil {
		if _, err := task.Delete(ctx, client.WithProcessKill); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("deleting the task: %w", err)
		}
	}

	if err := container.Delete(ctx, client.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("deleting the container: %w", err)
	}

	if err := os.Remove(c.logPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the log: %w", err)
	}

	return nil
}

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// Status reports where a workload stands. A container whose task was never
// created is still being set up; a stopped task succeeded on exit zero, and
// was cancelled if Cancel stopped it.
func (c *Containerd) Status(ctx context.Context, id string) (execution.Status, error) {
	container, task, err := c.load(ctx, id)
	if err != nil {
		return execution.Status{}, err
	}

	labels, err := container.Labels(ctx)
	if err != nil {
		return execution.Status{}, fmt.Errorf("reading labels: %w", err)
	}

	out := execution.Status{State: execution.StateAccepted, ProviderID: id, UpdatedAt: time.Now()}
	out.StartedAt, _ = time.Parse(time.RFC3339Nano, labels[labelStarted])

	if task == nil {
		return out, nil
	}

	status, err := task.Status(ctx)
	if err != nil {
		return execution.Status{}, fmt.Errorf("reading the task: %w", err)
	}

	switch status.Status {
	case client.Created:
		out.State = execution.StateAccepted
	case client.Stopped:
		out.EndedAt = status.ExitTime
		out.State = terminal(status.ExitStatus, labels[labelCancelled] == "true")
	default:
		out.State = execution.StateRunning
	}

	return out, nil
}

// terminal maps how a stopped task ended to a terminal state.
func terminal(exit uint32, cancelled bool) execution.State {
	switch {
	case cancelled:
		return execution.StateCancelled
	case exit == 0:
		return execution.StateSucceeded
	default:
		return execution.StateFailed
	}
}

// Result returns what a finished workload produced: its exit code, how long it
// ran, and the tail of its output.
func (c *Containerd) Result(ctx context.Context, id string) (*execution.Result, error) {
	status, err := c.Status(ctx, id)
	if err != nil {
		return nil, err
	}

	if !status.State.Terminal() {
		return nil, fmt.Errorf("workload %s has not finished", id)
	}

	_, task, err := c.load(ctx, id)
	if err != nil {
		return nil, err
	}

	exit, err := task.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the task: %w", err)
	}

	logs, truncated, err := c.tail(id)
	if err != nil {
		return nil, err
	}

	code := int(exit.ExitStatus)

	result := &execution.Result{ExitCode: &code, Logs: logs, LogsTruncated: truncated}
	if !status.StartedAt.IsZero() {
		result.Duration = status.EndedAt.Sub(status.StartedAt)
	}

	return result, nil
}

// Logs copies a workload's output to w as it is written, until the workload
// ends or ctx does.
func (c *Containerd) Logs(ctx context.Context, id string, w io.Writer) error {
	file, err := os.Open(c.logPath(id))
	if err != nil {
		return fmt.Errorf("opening the log of %s: %w", id, err)
	}

	defer func() { _ = file.Close() }()

	for {
		if _, err := io.Copy(w, file); err != nil {
			return err
		}

		status, err := c.Status(ctx, id)
		if err != nil {
			return err
		}

		if status.State.Terminal() {
			_, err := io.Copy(w, file)

			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(logPoll):
		}
	}
}

// Held returns every workload the executor holds, with what it declared and
// whether it is still using it: running, or created and about to.
func (c *Containerd) Held(ctx context.Context) ([]Held, error) {
	containers, err := c.client.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	held := make([]Held, 0, len(containers))

	for _, container := range containers {
		labels, err := container.Labels(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading labels of %s: %w", container.ID(), err)
		}

		h := Held{ID: container.ID(), Running: true}
		h.CPU, _ = strconv.ParseInt(labels[labelCPU], 10, 64)
		h.Memory, _ = strconv.ParseInt(labels[labelMemory], 10, 64)

		if task, err := container.Task(ctx, nil); err == nil {
			if status, err := task.Status(ctx); err == nil && status.Status == client.Stopped {
				h.Running = false
			}
		}

		held = append(held, h)
	}

	return held, nil
}

// Recover watches the timeouts of every workload still running, after the
// agent restarts.
func (c *Containerd) Recover(ctx context.Context) error {
	containers, err := c.client.Containers(ctx)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	for _, container := range containers {
		id := container.ID()

		labels, err := container.Labels(ctx)
		if err != nil {
			continue
		}

		timeout, _ := time.ParseDuration(labels[labelTimeout])
		started, _ := time.Parse(time.RFC3339Nano, labels[labelStarted])

		c.watch(id, timeout, started)
	}

	return nil
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// load returns a workload's container and its task, the task nil when none was
// created. An unknown workload is ErrUnknown.
func (c *Containerd) load(ctx context.Context, id string) (client.Container, client.Task, error) {
	container, err := c.client.LoadContainer(ctx, id)
	if errdefs.IsNotFound(err) {
		return nil, nil, fmt.Errorf("%w: %s", ErrUnknown, id)
	}

	if err != nil {
		return nil, nil, fmt.Errorf("loading %s: %w", id, err)
	}

	task, err := container.Task(ctx, nil)
	if errdefs.IsNotFound(err) {
		return container, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("loading the task of %s: %w", id, err)
	}

	return container, task, nil
}

// watch stops a workload that runs past its timeout, measured from when it
// started. One watcher per workload, and none without a timeout.
func (c *Containerd) watch(id string, timeout time.Duration, started time.Time) {
	if timeout <= 0 || started.IsZero() {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.watched[id] {
		return
	}

	c.watched[id] = true

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.watched, id)
			c.mu.Unlock()
		}()

		ctx := context.Background()

		_, task, err := c.load(ctx, id)
		if err != nil || task == nil {
			return
		}

		exited, err := task.Wait(ctx)
		if err != nil {
			return
		}

		select {
		case <-exited:
		case <-time.After(time.Until(started.Add(timeout))):
			_ = c.stop(ctx, task)
		}
	}()
}

// logPath is where a workload's output is written.
func (c *Containerd) logPath(id string) string {
	return filepath.Join(c.cfg.DataDir, "logs", id+".log")
}

// tail reads the last maxTailBytes of a workload's output, reporting whether
// anything before them was cut.
func (c *Containerd) tail(id string) ([]byte, bool, error) {
	data, err := os.ReadFile(c.logPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("reading the log of %s: %w", id, err)
	}

	if len(data) <= maxTailBytes {
		return data, false, nil
	}

	return data[len(data)-maxTailBytes:], true, nil
}
