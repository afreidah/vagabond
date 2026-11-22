// -------------------------------------------------------------------------------
// Vagabond Server
//
// Author: Alex Freidah
//
// The server's dependencies, construction, route table, and lifecycle. Routes
// live in their own files by resource; this file wires them together.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/nodes"
	"github.com/afreidah/vagabond/internal/state"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

const (
	shutdownTimeout = 10 * time.Second // in-flight requests get this long to finish
	headerTimeout   = 10 * time.Second
)

// -------------------------------------------------------------------------
// INTERFACE
// -------------------------------------------------------------------------

//go:generate mockgen -source=server.go -destination=mocks_test.go -package=server

// serverRegistry is the part of the provider registry the server reads:
// admission inputs, plugins by name, which namespaces exist, refreshing what
// providers can do, and which providers operators disabled through the API.
type serverRegistry interface {
	dispatch.Registry
	HasNamespace(namespace string) bool
	Refresh(ctx context.Context) error
	SetDisabled(disabled map[string]state.Disabled)
}

// serverLedger is the account dispatch charges, and when its usage snapshot
// was last read, which health reports.
type serverLedger interface {
	dispatch.Ledger
	Refreshed() time.Time
}

// serverExecutions is where runs and attempts are recorded, read back and
// listed, where dispatches a dead owner left are claimed and cancels are
// requested, where stored output is read and pruned, and whose reachability
// health reports. A schedule that prohibits overlap asks it whether a job
// still has a dispatch running.
type serverExecutions interface {
	dispatch.Executions
	GetDispatch(ctx context.Context, id execution.ID) (*execution.Dispatch, error)
	RunningDispatch(ctx context.Context, namespace, job string) (bool, error)
	ListExecutions(ctx context.Context, f execution.Filter) ([]*execution.Record, error)
	ClaimDispatches(
		ctx context.Context, owner string, now, until time.Time, skip []execution.ID,
	) ([]*execution.Dispatch, error)
	RequestCancel(ctx context.Context, id execution.ID, at time.Time) error
	ReadLog(ctx context.Context, id execution.ID, fromSeq int64, w io.Writer) error
	PruneLogs(ctx context.Context, before time.Time) (int, error)
	Ping(ctx context.Context) error
}

// serverJobs is where registered jobs live: storing versions and reading them
// back, listing the jobs that launch on a schedule, and claiming each
// scheduled launch so only one server makes it.
type serverJobs interface {
	jobs.Reader
	Register(
		ctx context.Context, namespace, name string, source []byte, periodic *job.Periodic, now time.Time,
	) (int64, bool, error)
	Jobs(ctx context.Context, namespace string) ([]*jobs.Job, error)
	Versions(ctx context.Context, namespace, name string) ([]*jobs.Version, error)
	Stop(ctx context.Context, namespace, name string, now time.Time) error
	PeriodicJobs(ctx context.Context) ([]*jobs.Job, error)
	ClaimLaunch(ctx context.Context, j *jobs.Job, now time.Time) (bool, error)
}

// serverProviders is where providers turned off through the API are kept, so
// every server sharing the store refuses them and a restart keeps them off.
type serverProviders interface {
	DisableProvider(ctx context.Context, name string, d state.Disabled) error
	EnableProvider(ctx context.Context, name string) error
	DisabledProviders(ctx context.Context) (map[string]state.Disabled, error)
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Server serves the API over one set of stores. running holds the cancel
// function of every dispatch this process is running, started or resumed, and
// nodeConns every client node connected to it, by name.
type Server struct {
	registry   serverRegistry
	ledger     serverLedger
	executions serverExecutions
	jobs       serverJobs
	providers  serverProviders
	dispatcher *dispatch.Dispatcher
	owner      string        // who this server's dispatches are leased to
	leaseTTL   time.Duration // how long a claimed lease runs unrenewed
	leaseRenew time.Duration // how often held leases are renewed
	claimEvery time.Duration // how often lapsed leases are claimed
	logger     *slog.Logger
	now        func() time.Time
	tiers      job.TierMode // the deployment's tier mode; empty is the default

	maxLogBytes int64         // how much of an execution's output is kept
	retainLogs  time.Duration // how long output is kept after its execution ends

	followPoll   time.Duration
	followLinger time.Duration
	stopping     chan struct{} // closed when shutdown begins, ending every live output stream
	stopOnce     sync.Once

	// A register or stop on this server sends here, so the next scheduled
	// launch is worked out again at once.
	wake chan struct{}

	mu      sync.Mutex
	running map[execution.ID]context.CancelFunc

	nodeConns *nodes.Conns
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// Option configures a Server.
type Option func(*Server)

// WithOwner names who this server's dispatches are leased to, in place of
// dispatch.ProcessOwner("server"). Two servers in one process need different
// owners to tell their leases apart.
func WithOwner(owner string) Option {
	return func(s *Server) { s.owner = owner }
}

// WithLeases replaces how long leases run, how often they are renewed, and how
// often lapsed ones are claimed. For tests that cannot wait a minute for a
// lease to lapse.
func WithLeases(ttl, renew, claim time.Duration) Option {
	return func(s *Server) { s.leaseTTL, s.leaseRenew, s.claimEvery = ttl, renew, claim }
}

// WithNodes shares conns, the agent nodes the server tracks, with whatever else
// reads them: containerd providers, built before the server.
func WithNodes(conns *nodes.Conns) Option {
	return func(s *Server) { s.nodeConns = conns }
}

// WithTierMode sets how provider tiers order candidates, in plans and
// dispatches, for a job that does not say.
func WithTierMode(mode job.TierMode) Option {
	return func(s *Server) { s.tiers = mode }
}

// WithLogs sets how much of each execution's output is kept and for how long
// after it ends, in place of execution.DefaultMaxLogBytes and
// execution.DefaultLogRetention.
func WithLogs(maxBytes int64, retain time.Duration) Option {
	return func(s *Server) { s.maxLogBytes, s.retainLogs = maxBytes, retain }
}

// New builds a server over its stores, with a dispatcher that records to
// executions and charges ledger.
func New(
	reg serverRegistry, ledger serverLedger, executions serverExecutions, jobStore serverJobs,
	providers serverProviders, logger *slog.Logger, opts ...Option,
) *Server {
	s := &Server{
		registry:     reg,
		ledger:       ledger,
		executions:   executions,
		jobs:         jobStore,
		providers:    providers,
		owner:        dispatch.ProcessOwner("server"),
		leaseTTL:     dispatch.LeaseTTL,
		leaseRenew:   dispatch.LeaseRenew,
		claimEvery:   claimInterval,
		logger:       logger,
		now:          time.Now,
		maxLogBytes:  execution.DefaultMaxLogBytes,
		retainLogs:   execution.DefaultLogRetention,
		followPoll:   defaultFollowPoll,
		followLinger: defaultFollowLinger,
		stopping:     make(chan struct{}),
		wake:         make(chan struct{}, 1),
		running:      make(map[execution.ID]context.CancelFunc),
		nodeConns:    nodes.New(),
	}

	for _, opt := range opts {
		opt(s)
	}

	s.dispatcher = dispatch.New(reg, ledger, executions,
		dispatch.WithOwner(s.owner), dispatch.WithLease(s.leaseTTL, s.leaseRenew), dispatch.WithTierMode(s.tiers),
		dispatch.WithMaxLogBytes(s.maxLogBytes), dispatch.WithLogger(s.logger))

	return s
}

// Handler returns the route table behind panic recovery and the request log.
func (s *Server) Handler() http.Handler {
	return s.logRequests(s.recoverRequests(s.routes()))
}

// routes is the route table. The job routes take ?namespace=, defaulting to
// the default namespace.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/jobs", s.wrap(s.registerJob))
	mux.HandleFunc("GET /v1/jobs", s.wrap(s.listJobs))
	mux.HandleFunc("POST /v1/jobs/run", s.wrap(s.runJob))
	mux.HandleFunc("POST /v1/jobs/plan", s.wrap(s.planJob))
	mux.HandleFunc("GET /v1/job/{name}", s.wrap(s.jobStatus))
	mux.HandleFunc("DELETE /v1/job/{name}", s.wrap(s.stopJob))
	mux.HandleFunc("POST /v1/job/{name}/dispatch", s.wrap(s.dispatchJob))
	mux.HandleFunc("GET /v1/dispatch/{id}", s.wrap(s.dispatchStatus))
	mux.HandleFunc("GET /v1/executions", s.wrap(s.listExecutions))
	mux.HandleFunc("GET /v1/execution/{id}", s.wrap(s.executionStatus))
	mux.HandleFunc("GET /v1/execution/{id}/logs", s.executionLogs)
	mux.HandleFunc("DELETE /v1/execution/{id}", s.wrap(s.cancelExecution))
	mux.HandleFunc("GET "+healthPath, s.health)
	mux.HandleFunc("GET /v1/nodes", s.wrap(s.listNodes))
	mux.HandleFunc("GET /v1/providers", s.wrap(s.listProviders))
	mux.HandleFunc("POST /v1/provider/{name}/disable", s.wrap(s.disableProvider))
	mux.HandleFunc("POST /v1/provider/{name}/enable", s.wrap(s.enableProvider))

	return mux
}

// -------------------------------------------------------------------------
// LIFECYCLE
// -------------------------------------------------------------------------

// Serve listens on addr until ctx is done, then drains requests and returns. A
// nil tlsConfig serves plain HTTP.
func (s *Server) Serve(ctx context.Context, addr string, tlsConfig *tls.Config) error {
	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	return s.ServeListener(ctx, listener, tlsConfig)
}

// ServeListener serves on an existing listener until ctx is done. Before
// serving it reads which providers are disabled, claims dispatches a dead
// owner left, and reaps stale reservations, then upkeep runs on its timers
// until the server stops. A tlsConfig with client CAs requires a verified
// client certificate on every route but health.
func (s *Server) ServeListener(ctx context.Context, listener net.Listener, tlsConfig *tls.Config) error {
	// Disabled providers first, so the dispatches claimed next already avoid them.
	s.tick(ctx, disabledTask, s.refreshDisabled)
	s.tick(ctx, claimTask, s.claim)
	s.tick(ctx, reapTask, s.reap)

	upkeepCtx, stopUpkeep := context.WithCancel(ctx)
	upkeep := s.startUpkeep(upkeepCtx)

	defer upkeep.Wait()
	defer stopUpkeep()

	var routes http.Handler = s.routes()
	if tlsConfig != nil && tlsConfig.ClientCAs != nil {
		routes = requireCertificate(routes)
	}

	httpServer := &http.Server{
		Handler:           s.logRequests(s.recoverRequests(routes)),
		ReadHeaderTimeout: headerTimeout,
		TLSConfig:         tlsConfig,
	}

	// A live output stream would otherwise hold shutdown open until it timed out.
	httpServer.RegisterOnShutdown(s.endStreams)

	errs := make(chan error, 1)

	go func() {
		if tlsConfig != nil {
			errs <- httpServer.ServeTLS(listener, "", "")
		} else {
			errs <- httpServer.Serve(listener)
		}
	}()

	s.logger.InfoContext(ctx, "serving", "address", listener.Addr().String(), "tls", tlsConfig != nil)

	select {
	case err := <-errs:
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
	}

	return s.shutdown(ctx, httpServer, errs)
}

// shutdown drains in-flight requests. Running dispatches are left alone: their
// leases lapse when the process exits, and the next server resumes them.
func (s *Server) shutdown(ctx context.Context, httpServer *http.Server, errs <-chan error) error {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}

	if err := <-errs; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving: %w", err)
	}

	s.logger.InfoContext(ctx, "stopped")

	return nil
}
