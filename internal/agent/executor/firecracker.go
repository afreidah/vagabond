// -------------------------------------------------------------------------------
// Firecracker Executor
//
// Author: Alex Freidah
//
// Runs workloads in Firecracker microVMs through vagabond-firecracker, the
// daemon beside the agent. The executor pulls each image through the node's
// containerd into a devmapper snapshot, a block device the microVM boots its
// root from, and resolves the image's process as containerd would; the daemon
// runs it and keeps every record, so its microVMs outlive the agent.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/errdefs"
	"github.com/containerd/ttrpc"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/backoff"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// snapshotter is containerd's snapshotter whose snapshots are block devices,
// which a microVM boots its root from.
const snapshotter = "devmapper"

// How soon a daemon that could not be dialled is dialled again.
const (
	redialMin = time.Second
	redialMax = 30 * time.Second
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// FirecrackerConfig is where the executor finds vagabond-firecracker and
// containerd, the containerd namespace its images' snapshots are kept in, and
// the cgroup every microVM is created under.
type FirecrackerConfig struct {
	Socket     string
	Containerd string
	Namespace  string
	Cgroup     string
}

// Firecracker runs workloads through one vagabond-firecracker. daemon is its
// client, nil until dialled and again once its connection has closed; after a
// dial fails, failures counts how many in a row, and none is tried before
// retryAt.
type Firecracker struct {
	cfg        FirecrackerConfig
	containerd *client.Client

	mu       sync.Mutex
	conn     *ttrpc.Client
	daemon   agentrpc.ExecutorService
	failures int
	retryAt  time.Time
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// NewFirecracker connects to containerd. The daemon is dialled on the first
// call, which Info makes at startup.
func NewFirecracker(cfg FirecrackerConfig) (*Firecracker, error) {
	c, err := client.New(cfg.Containerd, client.WithDefaultNamespace(cfg.Namespace))
	if err != nil {
		return nil, fmt.Errorf("connecting to containerd at %s: %w", cfg.Containerd, err)
	}

	return &Firecracker{cfg: cfg, containerd: c}, nil
}

// Close disconnects from the daemon and containerd. MicroVMs keep running.
func (f *Firecracker) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.conn != nil {
		_ = f.conn.Close()
	}

	return f.containerd.Close()
}

// Info is the daemon's account of itself, once containerd's devmapper
// snapshotter has loaded, without which no microVM has a root.
func (f *Firecracker) Info(ctx context.Context) (*agentrpc.NodeExecutor, error) {
	if err := f.devmapper(ctx); err != nil {
		return nil, err
	}

	info, err := call(ctx, f, func(d agentrpc.ExecutorService) (*agentrpc.NodeExecutor, error) {
		return d.Info(ctx, &agentrpc.Empty{})
	})
	if err != nil {
		return nil, fmt.Errorf("vagabond-firecracker at %s does not answer: %w", f.cfg.Socket, err)
	}

	return info, nil
}

// devmapper reports containerd's devmapper snapshotter missing, or failed
// when containerd started.
func (f *Firecracker) devmapper(ctx context.Context) error {
	found, err := f.containerd.IntrospectionService().Plugins(ctx, "id=="+snapshotter)
	if err != nil {
		return fmt.Errorf("asking containerd at %s for its plugins: %w", f.cfg.Containerd, err)
	}

	// IDs are unique within a type only, so the type is checked here.
	for _, p := range found.GetPlugins() {
		if p.GetType() != string(plugins.SnapshotPlugin) {
			continue
		}

		if p.GetInitErr() != nil {
			return fmt.Errorf("containerd's %s snapshotter did not load: %s", snapshotter, p.GetInitErr().GetMessage())
		}

		return nil
	}

	return fmt.Errorf("containerd at %s has no %s snapshotter", f.cfg.Containerd, snapshotter)
}

// -------------------------------------------------------------------------
// RUNNING
// -------------------------------------------------------------------------

// Submit pulls the image into a devmapper snapshot and has the daemon boot a
// microVM from it under id. One the daemon already holds is left as it is,
// since the ID is the idempotency key; one the daemon refuses leaves nothing
// behind.
func (f *Firecracker) Submit(ctx context.Context, id string, w *agentrpc.Workload) error {
	if _, err := f.Status(ctx, id); !errors.Is(err, ErrUnknown) {
		return err
	}

	// A container of id with no microVM is left from a submission that failed
	// before it reached the daemon.
	if err := f.remove(ctx, id); err != nil {
		return err
	}

	container, err := create(ctx, f.containerd, id, w, snapshotter, nil)
	if err != nil {
		return err
	}

	if err := f.boot(ctx, container, w); err != nil {
		_ = f.remove(context.WithoutCancel(ctx), id)

		return err
	}

	return nil
}

// boot hands the daemon the workload's process, as containerd resolved it
// from the image, and the snapshot's block device as its root.
func (f *Firecracker) boot(ctx context.Context, container client.Container, w *agentrpc.Workload) error {
	spec, err := container.Spec(ctx)
	if err != nil {
		return fmt.Errorf("reading the spec of %s: %w", container.ID(), err)
	}

	// A devmapper snapshot mounts as one block device of one filesystem type.
	mounts, err := f.containerd.SnapshotService(snapshotter).Mounts(ctx, container.ID())
	if err != nil {
		return fmt.Errorf("reading the root drive of %s: %w", container.ID(), err)
	}

	if len(mounts) == 0 {
		return fmt.Errorf("the snapshot of %s has no root drive", container.ID())
	}

	_, err = call(ctx, f, func(d agentrpc.ExecutorService) (*agentrpc.Submission, error) {
		return d.Submit(ctx, &agentrpc.ExecutorSubmitRequest{
			ExecutionId: container.ID(),
			Process:     processOf(spec.Process),
			Resources:   w.GetResources(),
			Timeout:     w.GetTimeout(),
			RootDrive:   mounts[0].Source,
			RootFstype:  mounts[0].Type,
			Cgroup:      f.cfg.Cgroup,
		})
	})

	return err
}

// processOf is the process a container's spec runs, as the daemon takes it.
// A variable set twice keeps its last value, as a shell would.
func processOf(p *specs.Process) *agentrpc.Process {
	env := make(map[string]string, len(p.Env))
	for _, kv := range p.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}

	return &agentrpc.Process{
		Args:           p.Args,
		Env:            env,
		WorkingDir:     p.Cwd,
		Uid:            p.User.UID,
		Gid:            p.User.GID,
		AdditionalGids: p.User.AdditionalGids,
	}
}

// Cancel has the daemon stop a workload, which then reports cancelled.
func (f *Firecracker) Cancel(ctx context.Context, id string) error {
	_, err := call(ctx, f, func(d agentrpc.ExecutorService) (*agentrpc.Empty, error) {
		return d.Cancel(ctx, &agentrpc.ExecutionRequest{ExecutionId: id})
	})

	return err
}

// Release has the daemon remove a microVM and what it kept for it, then
// deletes its container and snapshot. One neither holds is released already.
func (f *Firecracker) Release(ctx context.Context, id string) error {
	_, err := call(ctx, f, func(d agentrpc.ExecutorService) (*agentrpc.Empty, error) {
		return d.Release(ctx, &agentrpc.ExecutionRequest{ExecutionId: id})
	})
	if err != nil {
		return err
	}

	return f.remove(ctx, id)
}

// remove deletes id's container and its snapshot. One already gone counts as
// deleted.
func (f *Firecracker) remove(ctx context.Context, id string) error {
	container, err := f.containerd.LoadContainer(ctx, id)
	if errdefs.IsNotFound(err) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("loading %s: %w", id, err)
	}

	if err := container.Delete(ctx, client.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("deleting the container of %s: %w", id, err)
	}

	return nil
}

// -------------------------------------------------------------------------
// READING
// -------------------------------------------------------------------------

// Status reports where a workload stands, as the daemon has it.
func (f *Firecracker) Status(ctx context.Context, id string) (*agentrpc.ExecutionStatus, error) {
	return call(ctx, f, func(d agentrpc.ExecutorService) (*agentrpc.ExecutionStatus, error) {
		return d.Status(ctx, &agentrpc.ExecutionRequest{ExecutionId: id})
	})
}

// Result returns what a finished workload produced: its exit code, unset when
// the microVM died first, and how long it ran.
func (f *Firecracker) Result(ctx context.Context, id string) (*agentrpc.ExecutionResult, error) {
	return call(ctx, f, func(d agentrpc.ExecutorService) (*agentrpc.ExecutionResult, error) {
		return d.Result(ctx, &agentrpc.ExecutionRequest{ExecutionId: id})
	})
}

// Logs copies a workload's output from byte offset on to w, read from the
// daemon until all of it has been, and polled every logPoll while nothing new
// has come. One the daemon does not hold is ErrUnknown before anything is
// written; one released while it is followed has written all it will.
func (f *Firecracker) Logs(ctx context.Context, id string, offset int64, w io.Writer) error {
	for read := false; ; read = true {
		out, err := call(ctx, f, func(d agentrpc.ExecutorService) (*agentrpc.Output, error) {
			return d.ReadLogs(ctx, &agentrpc.LogsRequest{ExecutionId: id, Offset: offset})
		})

		switch {
		case read && errors.Is(err, ErrUnknown):
			return nil
		case err != nil:
			return err
		}

		// Written only when there is something, since a write sends a chunk.
		if data := out.GetData(); len(data) > 0 {
			n, err := w.Write(data)
			offset += int64(n)

			if err != nil {
				return err
			}
		}

		// Done once it has ended; with nothing new yet, wait for more, or for ctx.
		switch {
		case out.GetEnded():
			return nil
		case len(out.GetData()) == 0:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(logPoll):
			}
		}
	}
}

// Held is every microVM the daemon holds, each with its overhead counted.
func (f *Firecracker) Held(ctx context.Context) ([]*agentrpc.Held, error) {
	list, err := call(ctx, f, func(d agentrpc.ExecutorService) (*agentrpc.HeldList, error) {
		return d.Held(ctx, &agentrpc.Empty{})
	})
	if err != nil {
		return nil, err
	}

	return list.GetHeld(), nil
}

// -------------------------------------------------------------------------
// THE DAEMON'S CONNECTION
// -------------------------------------------------------------------------

// call makes one call to the daemon. A connection found closed is forgotten,
// so the next call dials again, and the daemon's NotFound is ErrUnknown.
func call[T any](ctx context.Context, f *Firecracker, do func(agentrpc.ExecutorService) (T, error)) (T, error) {
	var zero T

	daemon, err := f.dial(ctx)
	if err != nil {
		return zero, err
	}

	out, err := do(daemon)

	switch {
	case errors.Is(err, ttrpc.ErrClosed):
		f.forget(daemon)

		return zero, fmt.Errorf("vagabond-firecracker at %s: %w", f.cfg.Socket, err)
	case status.Code(err) == codes.NotFound:
		return zero, fmt.Errorf("%w: %s", ErrUnknown, status.Convert(err).Message())
	case err != nil:
		return zero, err
	}

	return out, nil
}

// dial returns the daemon's client, dialling the daemon when there is none.
// One that could not be dialled is not tried again until its backoff has
// passed, so calls in the meantime fail at once.
func (f *Firecracker) dial(ctx context.Context) (agentrpc.ExecutorService, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.daemon != nil {
		return f.daemon, nil
	}

	if wait := time.Until(f.retryAt); wait > 0 {
		return nil, fmt.Errorf("vagabond-firecracker at %s is not connected; dialling again in %s",
			f.cfg.Socket, wait.Round(time.Second))
	}

	// Each failure in a row waits twice as long before the next try.
	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "unix", f.cfg.Socket)
	if err != nil {
		f.retryAt = time.Now().Add(backoff.Exponential(redialMin, redialMax, f.failures))
		f.failures++

		return nil, fmt.Errorf("dialling vagabond-firecracker at %s: %w", f.cfg.Socket, err)
	}

	f.failures = 0
	f.conn = ttrpc.NewClient(conn)
	f.daemon = agentrpc.NewExecutorClient(f.conn)

	return f.daemon, nil
}

// forget drops the daemon's client after its connection closed, unless a
// newer one has replaced it already.
func (f *Firecracker) forget(daemon agentrpc.ExecutorService) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.daemon == daemon {
		_ = f.conn.Close()
		f.conn, f.daemon = nil, nil
	}
}
