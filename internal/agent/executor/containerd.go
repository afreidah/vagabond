// -------------------------------------------------------------------------------
// containerd Executor
//
// Author: Alex Freidah
//
// Runs workloads as containers on the node's containerd, in their own
// namespace, each hardened, in a user namespace of its own, and with a network
// of its own unless the agent runs them on the node's network. The container
// ID is the execution ID, output goes to a log file containerd writes itself,
// its front freed past the cap, and everything the executor needs to know
// about a workload is on the container as labels, so an agent that restarts
// finds its workloads, their state, their timeouts, addresses and user ID
// ranges where it left them.
// -------------------------------------------------------------------------------

package executor

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/logging"
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
	labelPrivate   = "vagabond.private"
	labelAddress   = "vagabond.address"
	labelIDRange   = "vagabond.idrange"
)

// stopGrace is how long a stopped workload gets between SIGTERM and SIGKILL.
const stopGrace = 10 * time.Second

// How often logs are cut back to the cap, and how much of one a read takes.
const (
	logTrim = 10 * time.Second
	logRead = 64 << 10
)

var (
	// ErrUnknown reports an execution this executor holds no workload for.
	ErrUnknown = errors.New("no such workload")

	// ErrInvalid reports a workload no node could run, such as one whose image
	// reference does not parse.
	ErrInvalid = errors.New("invalid workload")
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Config is where the executor runs workloads. Cgroup is the parent every
// workload's cgroup is created under. MaxLogBytes is how much of each
// workload's output is kept, execution.DefaultMaxLogBytes when zero, and
// Logger takes what goes wrong in the background, discarded when nil. CapAdd
// adds capabilities to the few every workload keeps, NoUserNS runs workloads
// without a user namespace of their own, and ReadOnlyRoot makes their root
// read-only with /tmp writable.
type Config struct {
	Socket       string
	Namespace    string
	DataDir      string
	Cgroup       string
	MaxLogBytes  int64
	Network      NetworkConfig
	CapAdd       []string
	NoUserNS     bool
	ReadOnlyRoot bool
	Logger       *slog.Logger
}

// Containerd runs workloads on one containerd. net is nil when workloads share
// the node's network. taking holds the user ID ranges of containers being
// made, which no label shows yet.
type Containerd struct {
	client  *client.Client
	cfg     Config
	net     *network
	caps    []string
	mu      sync.Mutex
	watched map[string]bool
	untrim  context.CancelFunc
	ranges  sync.Mutex
	taking  map[int]bool
}

// workloadLabels are a container's labels read back: when its task started,
// its timeout, whether Cancel stopped it, what it declared, whether it
// reaches the node's private networks from its address, and its user ID
// range, -1 for none.
type workloadLabels struct {
	started   time.Time
	timeout   time.Duration
	cancelled bool
	declared  *agentrpc.Resources
	private   bool
	address   netip.Addr
	idRange   int
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New checks the capabilities to add, connects to containerd, makes the log
// directory, readies workloads' networks unless they share the node's, and
// starts cutting logs back to the cap.
func New(cfg *Config) (*Containerd, error) {
	caps, err := capabilities(cfg.CapAdd)
	if err != nil {
		return nil, err
	}

	cl, err := client.New(cfg.Socket, client.WithDefaultNamespace(cfg.Namespace))
	if err != nil {
		return nil, fmt.Errorf("connecting to containerd at %s: %w", cfg.Socket, err)
	}

	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "logs"), 0o750); err != nil {
		_ = cl.Close()

		return nil, fmt.Errorf("creating the log directory: %w", err)
	}

	var nw *network

	if !cfg.Network.Host {
		if nw, err = newNetwork(&cfg.Network, cfg.DataDir, cfg.Namespace); err != nil {
			_ = cl.Close()

			return nil, err
		}
	}

	ctx, untrim := context.WithCancel(context.Background())

	c := &Containerd{
		client: cl, cfg: *cfg, net: nw, caps: caps, untrim: untrim,
		watched: make(map[string]bool), taking: make(map[int]bool),
	}
	c.cfg.MaxLogBytes = cmp.Or(c.cfg.MaxLogBytes, execution.DefaultMaxLogBytes)
	c.cfg.Logger = logging.Named(c.cfg.Logger, "executor")

	go c.trim(ctx)

	return c, nil
}

// Close stops cutting logs back and disconnects from containerd, and may be
// called again. Workloads keep running.
func (c *Containerd) Close() error {
	c.untrim()

	return c.client.Close()
}

// Info names the executor, once containerd answers and, unless user namespaces
// are off, can mount images ID-mapped. Its workloads take nothing of the node
// beyond what they declare.
func (c *Containerd) Info(ctx context.Context) (*agentrpc.NodeExecutor, error) {
	if _, err := c.client.Version(ctx); err != nil {
		return nil, fmt.Errorf("containerd at %s does not answer: %w", c.cfg.Socket, err)
	}

	if !c.cfg.NoUserNS {
		if err := c.idMapped(ctx); err != nil {
			return nil, err
		}
	}

	return &agentrpc.NodeExecutor{Name: agentrpc.ExecutorContainerd}, nil
}

// -------------------------------------------------------------------------
// RUNNING
// -------------------------------------------------------------------------

// Submit pulls the image and starts the workload under id, in a user ID range
// of its own. A workload already under id is left as it is, since the ID is
// the idempotency key; one that fails to start leaves nothing behind, so a
// retry starts it afresh.
func (c *Containerd) Submit(ctx context.Context, id string, w *agentrpc.Workload) error {
	if _, err := c.client.LoadContainer(ctx, id); err == nil {
		return nil
	}

	n, release, err := c.takeRange(ctx)
	if err != nil {
		return err
	}

	defer release()

	container, err := create(ctx, c.client, id, w, rootfs{opts: snapshotFor(n)}, c.limits(id, w, n),
		client.WithContainerLabels(map[string]string{
			labelTimeout: w.GetTimeout().AsDuration().String(),
			labelCPU:     strconv.FormatInt(w.GetResources().GetCpu(), 10),
			labelMemory:  strconv.FormatInt(w.GetResources().GetMemory(), 10),
			labelPrivate: strconv.FormatBool(w.GetPrivateNetwork()),
			labelIDRange: strconv.Itoa(n),
		}),
	)
	if err != nil {
		return err
	}

	if err := c.start(ctx, container); err != nil {
		_ = c.remove(context.WithoutCancel(ctx), container)

		return err
	}

	c.watch(id, w.GetTimeout().AsDuration(), time.Now())

	return nil
}

// start creates the container's task writing to its log, attaches its network
// before anything runs in it, labels the container with when it started and
// its address, and starts it.
func (c *Containerd) start(ctx context.Context, container client.Container) error {
	task, err := container.NewTask(ctx, cio.LogFile(c.logPath(container.ID())))
	if err != nil {
		return fmt.Errorf("creating the task: %w", err)
	}

	labels := map[string]string{labelStarted: time.Now().UTC().Format(time.RFC3339Nano)}

	if c.net != nil {
		addr, err := c.net.attach(ctx, container.ID(), task.Pid())
		if err != nil {
			return err
		}

		labels[labelAddress] = addr.String()
	}

	if _, err := container.SetLabels(ctx, labels); err != nil {
		return fmt.Errorf("labelling the container: %w", err)
	}

	// Rewritten after the address label is set, so a private container is exempt before it runs
	if err := c.rules(ctx); err != nil {
		return err
	}

	if err := task.Start(ctx); err != nil {
		return fmt.Errorf("starting the task: %w", err)
	}

	return nil
}

// limits holds a workload to what it declared, under the executor's cgroup,
// hardened, in the user ID range idRange. runc gives it a new network
// namespace unless the agent uses the node's network, where a workload in a
// user namespace gets the node's /sys. Isolated workloads resolve through the
// configured name server; private and host-network ones use the node's
// resolv.conf.
func (c *Containerd) limits(id string, w *agentrpc.Workload, idRange int) []oci.SpecOpts {
	opts := append([]oci.SpecOpts{oci.WithCgroup(filepath.Join(c.cfg.Cgroup, id)), oci.WithHostHostsFile}, c.hardening(idRange)...)

	if memory := w.GetResources().GetMemory(); memory > 0 {
		opts = append(opts, oci.WithMemoryLimit(uint64(memory)<<20)) //nolint:gosec // positive
	}

	if cpu := w.GetResources().GetCpu(); cpu > 0 {
		opts = append(opts, oci.WithCPUCFS(cpu*100, 100000))
	}

	switch {
	case c.net == nil && idRange >= 0:
		return append(opts, oci.WithHostNamespace(specs.NetworkNamespace), oci.WithHostResolvconf, withNodeSys)
	case c.net == nil:
		return append(opts, oci.WithHostNamespace(specs.NetworkNamespace), oci.WithHostResolvconf)
	case w.GetPrivateNetwork():
		return append(opts, oci.WithHostResolvconf)
	default:
		return append(opts, oci.WithMounts([]specs.Mount{{
			Destination: "/etc/resolv.conf", Type: "bind", Source: c.net.resolvConf, Options: []string{"rbind", "ro"},
		}}))
	}
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

// Release deletes a workload and everything kept for it. One the executor does
// not hold is released already.
func (c *Containerd) Release(ctx context.Context, id string) error {
	container, err := c.client.LoadContainer(ctx, id)
	if errdefs.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("loading %s: %w", id, err)
	}

	return c.remove(ctx, container)
}

// remove deletes a workload: its task, killing it if it still runs, its
// network address, its container and snapshot, and its log. Parts already
// gone are skipped.
func (c *Containerd) remove(ctx context.Context, container client.Container) error {
	task, err := container.Task(ctx, nil)

	switch {
	case err == nil:
		if _, err := task.Delete(ctx, client.WithProcessKill); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("deleting the task: %w", err)
		}
	case !errdefs.IsNotFound(err):
		return fmt.Errorf("loading the task: %w", err)
	}

	// Freed before the container goes, so a failure leaves it for the retry
	if c.net != nil {
		if err := c.net.detach(ctx, container.ID()); err != nil {
			return err
		}
	}

	if err := container.Delete(ctx, client.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("deleting the container: %w", err)
	}

	if err := os.Remove(c.logPath(container.ID())); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the log: %w", err)
	}

	return nil
}

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// Status reports where a workload stands.
func (c *Containerd) Status(ctx context.Context, id string) (*agentrpc.ExecutionStatus, error) {
	st, err := c.status(ctx, id)
	if err != nil {
		return nil, err
	}

	return agentrpc.StatusOf(&st), nil
}

// status reports where a workload stands. A container whose task was never
// created is still being set up, and one whose task went after it started, as
// when the host restarts, failed. A stopped task succeeded on exit zero, and
// was cancelled if Cancel stopped it.
func (c *Containerd) status(ctx context.Context, id string) (execution.Status, error) {
	container, task, err := c.load(ctx, id)
	if err != nil {
		return execution.Status{}, err
	}

	labels, err := labelsOf(ctx, container)
	if err != nil {
		return execution.Status{}, err
	}

	out := execution.Status{State: execution.StateAccepted, ProviderID: id, UpdatedAt: time.Now(), StartedAt: labels.started}

	if task == nil {
		if !labels.started.IsZero() {
			out.State = execution.StateFailed
		}

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
		out.State = terminal(status.ExitStatus, labels.cancelled)
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

// Result returns what a finished workload produced: its exit code, unset when
// its task went without one, and how long it ran. Its output is read through
// Logs.
func (c *Containerd) Result(ctx context.Context, id string) (*agentrpc.ExecutionResult, error) {
	st, err := c.status(ctx, id)
	if err != nil {
		return nil, err
	}

	if !st.State.Terminal() {
		return nil, fmt.Errorf("workload %s has not finished", id)
	}

	_, task, err := c.load(ctx, id)
	if err != nil {
		return nil, err
	}

	res := &execution.Result{Duration: execution.Elapsed(st.StartedAt, st.EndedAt)}

	if task != nil {
		exit, err := task.Status(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading the task: %w", err)
		}

		res.ExitCode = new(int(exit.ExitStatus))
	}

	return agentrpc.ResultOf(res), nil
}

// Logs copies a workload's output from byte offset on to w as it is written,
// until the workload ends, is released, or ctx ends. One it does not hold is
// ErrUnknown before anything is written, so the caller can ask another
// executor; one with no log yet has written nothing.
func (c *Containerd) Logs(ctx context.Context, id string, offset int64, w execution.LogWriter) error {
	if _, _, err := c.load(ctx, id); err != nil {
		return err
	}

	file, err := os.Open(c.logPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("opening the log of %s: %w", id, err)
	}

	defer func() { _ = file.Close() }()

	buf := make([]byte, logRead)

	return follow(ctx, offset, w, func(offset int64) (output, error) {
		out, err := c.readLog(file, offset, buf)
		if err != nil || len(out.data) > 0 {
			return out, err
		}

		st, err := c.status(ctx, id)
		if err != nil && !errors.Is(err, ErrUnknown) {
			return output{}, err
		}

		if err == nil && !st.State.Terminal() {
			return out, nil
		}

		// Ended or released: the open file still holds whatever it wrote last
		out, err = c.readLog(file, offset, buf)
		out.ended = len(out.data) == 0

		return out, err
	})
}

// readLog reads into buf what of an open log follows offset, from where its
// kept part begins when offset is before it. A read overtaken by the front
// being dropped, which may have taken what it read, gives only where the kept
// part now begins.
func (c *Containerd) readLog(file *os.File, offset int64, buf []byte) (output, error) {
	start, err := c.keptStart(file)
	if err != nil {
		return output{}, err
	}

	offset = max(offset, start)

	n, err := file.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return output{}, fmt.Errorf("reading %s: %w", file.Name(), err)
	}

	if now, err := c.keptStart(file); err != nil || now > offset {
		return output{offset: now}, err
	}

	return output{data: buf[:n], offset: offset}, nil
}

// keptStart is where the kept part of an open log begins: past MaxLogBytes, its
// front is dropped in whole chunks, as the server drops stored output.
func (c *Containerd) keptStart(file *os.File) (int64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("reading the size of %s: %w", file.Name(), err)
	}

	return execution.KeepFrom(info.Size(), c.cfg.MaxLogBytes) * execution.LogChunk, nil
}

// trim cuts every workload's log back to the cap every logTrim until ctx ends,
// as the executor closes, so a workload printing without end cannot fill the
// disk.
func (c *Containerd) trim(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(logTrim):
		}

		// The pattern is fixed, so it cannot be malformed
		logs, _ := filepath.Glob(filepath.Join(c.cfg.DataDir, "logs", "*.log"))

		for _, path := range logs {
			if err := c.trimLog(path); err != nil {
				c.cfg.Logger.WarnContext(ctx, "trimming a workload's output failed", "error", err)
			}
		}
	}
}

// trimLog frees the front of one log, before where its kept part begins, as a
// hole: its length stays, so offsets into it still count from the first byte
// the workload printed. A log released meanwhile is left alone.
func (c *Containerd) trimLog(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	start, err := c.keptStart(file)
	if err != nil || start == 0 {
		return err
	}

	err = unix.Fallocate(int(file.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, 0, start)
	if err != nil {
		return fmt.Errorf("freeing the first %d bytes of %s: %w", start, path, err)
	}

	return nil
}

// Held returns every workload the executor holds, with what it declared and
// whether it is still using it: running, or created and about to, and not once
// its task stopped or went.
func (c *Containerd) Held(ctx context.Context) ([]*agentrpc.Held, error) {
	containers, err := c.client.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	held := make([]*agentrpc.Held, 0, len(containers))

	for _, container := range containers {
		labels, err := labelsOf(ctx, container)
		if err != nil {
			return nil, err
		}

		h := &agentrpc.Held{ExecutionId: container.ID(), Resources: labels.declared, Running: true}

		// No task is still being set up, or gone since it started; stopped is done.
		task, err := container.Task(ctx, nil)

		switch {
		case errdefs.IsNotFound(err):
			h.Running = labels.started.IsZero()
		case err == nil:
			status, err := task.Status(ctx)
			h.Running = err != nil || status.Status != client.Stopped
		}

		held = append(held, h)
	}

	return held, nil
}

// Recover restarts the timeout watch on every workload still running, and
// rewrites the firewall rules, which are gone if the node rebooted.
func (c *Containerd) Recover(ctx context.Context) error {
	containers, err := c.client.Containers(ctx)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	for _, container := range containers {
		labels, err := labelsOf(ctx, container)
		if err != nil {
			continue
		}

		c.watch(container.ID(), labels.timeout, labels.started)
	}

	return c.rules(ctx)
}

// rules rewrites the firewall rules for the container address pool, exempting
// from the private-network drops each container labelled as asking for them.
func (c *Containerd) rules(ctx context.Context) error {
	if c.net == nil {
		return nil
	}

	containers, err := c.client.Containers(ctx)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	var private []netip.Addr

	for _, container := range containers {
		labels, err := labelsOf(ctx, container)
		if err != nil {
			return err
		}

		if labels.private && labels.address.IsValid() {
			private = append(private, labels.address)
		}
	}

	return c.net.rules(ctx, private)
}

// -------------------------------------------------------------------------
// CONTAINERS
// -------------------------------------------------------------------------

// rootfs is how a container's root filesystem is made: by snapshotter,
// containerd's default when empty, with opts.
type rootfs struct {
	snapshotter string
	opts        []snapshots.Opt
}

// create pulls w's image, unpacked into root's snapshotter, and makes a
// container for it under id: a snapshot of the image as its root filesystem,
// made as root says, and a spec of the image's process with w's overrides,
// then spec. A bad image reference is ErrInvalid. Nothing is left behind when
// it fails, the snapshot made before the container included, which would
// otherwise stop a retry.
func create(
	ctx context.Context, cl *client.Client, id string, w *agentrpc.Workload, root rootfs,
	spec []oci.SpecOpts, opts ...client.NewContainerOpts,
) (client.Container, error) {
	ref, err := reference.ParseDockerRef(w.GetImage())
	if err != nil {
		return nil, fmt.Errorf("%w: image %q: %w", ErrInvalid, w.GetImage(), err)
	}

	image, err := cl.Pull(ctx, ref.String(), client.WithPullUnpack, client.WithPullSnapshotter(root.snapshotter))
	if err != nil {
		return nil, fmt.Errorf("pulling %s: %w", ref, err)
	}

	container, err := cl.NewContainer(ctx, id, append([]client.NewContainerOpts{
		client.WithSnapshotter(root.snapshotter),
		client.WithNewSnapshot(id, image, root.opts...),
		client.WithNewSpec(append(process(image, w), spec...)...),
	}, opts...)...)
	if err != nil {
		_ = cl.SnapshotService(root.snapshotter).Remove(context.WithoutCancel(ctx), id)

		return nil, fmt.Errorf("creating the container: %w", err)
	}

	return container, nil
}

// process is a workload's process: the image's entrypoint, command,
// environment, working directory and user, with w's overrides on top.
func process(image client.Image, w *agentrpc.Workload) []oci.SpecOpts {
	opts := []oci.SpecOpts{oci.WithImageConfig(image)}

	switch {
	case len(w.GetCommand()) > 0:
		opts = append(opts, oci.WithProcessArgs(slices.Concat(w.GetCommand(), w.GetArgs())...))
	case len(w.GetArgs()) > 0:
		opts = append(opts, oci.WithImageConfigArgs(image, w.GetArgs()))
	}

	if len(w.GetEnv()) > 0 {
		env := make([]string, 0, len(w.GetEnv()))
		for k, v := range w.GetEnv() {
			env = append(env, k+"="+v)
		}

		opts = append(opts, oci.WithEnv(env))
	}

	if w.GetWorkingDir() != "" {
		opts = append(opts, oci.WithProcessCwd(w.GetWorkingDir()))
	}

	return opts
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

// labelsOf reads a container's labels back. A value that does not parse reads
// as unset.
func labelsOf(ctx context.Context, container client.Container) (workloadLabels, error) {
	labels, err := container.Labels(ctx)
	if err != nil {
		return workloadLabels{}, fmt.Errorf("reading the labels of %s: %w", container.ID(), err)
	}

	out := workloadLabels{
		cancelled: labels[labelCancelled] == "true", private: labels[labelPrivate] == "true", declared: &agentrpc.Resources{},
	}
	out.address, _ = netip.ParseAddr(labels[labelAddress])

	// A container made before user namespaces, or without one, holds no range
	if out.idRange, err = strconv.Atoi(labels[labelIDRange]); err != nil {
		out.idRange = -1
	}
	out.started, _ = time.Parse(time.RFC3339Nano, labels[labelStarted])
	out.timeout, _ = time.ParseDuration(labels[labelTimeout])
	out.declared.Cpu, _ = strconv.ParseInt(labels[labelCPU], 10, 64)
	out.declared.Memory, _ = strconv.ParseInt(labels[labelMemory], 10, 64)

	return out, nil
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
