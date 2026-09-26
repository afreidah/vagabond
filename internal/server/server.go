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
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/quota"
	"github.com/afreidah/vagabond/internal/scheduler"
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
// admission inputs, plugins by name, which namespaces exist, and refreshing
// what providers can do.
type serverRegistry interface {
	Inputs(namespace string, usage func(namespace, provider string) (total, share quota.PoolUsage)) []scheduler.Input
	Provider(name string) (plugin.Provider, bool)
	HasNamespace(namespace string) bool
	Refresh(ctx context.Context) error
}

// serverExecutions is where runs and attempts are recorded and read back, and
// where dispatches a dead owner left are claimed.
type serverExecutions interface {
	dispatch.Executions
	GetDispatch(ctx context.Context, id execution.ID) (*execution.Dispatch, error)
	Get(ctx context.Context, id execution.ID) (*execution.Record, error)
	ClaimDispatches(ctx context.Context, owner string, now, until time.Time) ([]*execution.Dispatch, error)
}

// serverJobs is where registered jobs live: storing versions, reading them
// back, and the executions they produced.
type serverJobs interface {
	jobs.Reader
	Register(ctx context.Context, namespace, name string, source []byte, now time.Time) (int64, bool, error)
	Jobs(ctx context.Context, namespace string) ([]*jobs.Job, error)
	Versions(ctx context.Context, namespace, name string) ([]*jobs.Version, error)
	Stop(ctx context.Context, namespace, name string, now time.Time) error
	JobExecutions(ctx context.Context, namespace, job string, limit int) ([]*execution.Record, error)
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Server serves the API over one set of stores. running holds the cancel
// function of every dispatch this process is running, started or resumed.
type Server struct {
	registry   serverRegistry
	ledger     dispatch.Ledger
	executions serverExecutions
	jobs       serverJobs
	dispatcher *dispatch.Dispatcher
	owner      string // who this server's dispatches are leased to
	logger     *slog.Logger
	now        func() time.Time

	mu      sync.Mutex
	running map[execution.ID]context.CancelFunc
}

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// New builds a server over its stores, with a dispatcher that records to
// executions and charges ledger.
func New(
	reg serverRegistry, ledger dispatch.Ledger, executions serverExecutions, jobStore serverJobs, logger *slog.Logger,
) *Server {
	owner := dispatch.ProcessOwner("server")

	return &Server{
		registry:   reg,
		ledger:     ledger,
		executions: executions,
		jobs:       jobStore,
		dispatcher: dispatch.New(reg, ledger, executions, dispatch.WithOwner(owner)),
		owner:      owner,
		logger:     logger,
		now:        time.Now,
		running:    make(map[execution.ID]context.CancelFunc),
	}
}

// Handler returns the route table. Every route takes ?namespace=, defaulting to
// the default namespace.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/jobs", s.wrap(s.registerJob))
	mux.HandleFunc("GET /v1/jobs", s.wrap(s.listJobs))
	mux.HandleFunc("POST /v1/jobs/run", s.wrap(s.runJob))
	mux.HandleFunc("POST /v1/jobs/plan", s.wrap(s.planJob))
	mux.HandleFunc("GET /v1/job/{name}", s.wrap(s.jobStatus))
	mux.HandleFunc("DELETE /v1/job/{name}", s.wrap(s.stopJob))
	mux.HandleFunc("POST /v1/job/{name}/dispatch", s.wrap(s.dispatchJob))
	mux.HandleFunc("GET /v1/dispatch/{id}", s.wrap(s.dispatchStatus))
	mux.HandleFunc("GET /v1/execution/{id}", s.wrap(s.executionStatus))
	mux.HandleFunc("GET /v1/execution/{id}/logs", s.executionLogs)
	mux.HandleFunc("DELETE /v1/execution/{id}", s.wrap(s.cancelExecution))

	return s.logRequests(mux)
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

// ServeListener serves on an existing listener until ctx is done. Dispatches a
// dead owner left are claimed and stale reservations reaped before serving,
// then upkeep runs on its timers until the server stops.
func (s *Server) ServeListener(ctx context.Context, listener net.Listener, tlsConfig *tls.Config) error {
	s.claim(ctx)
	s.reap(ctx)

	upkeepCtx, stopUpkeep := context.WithCancel(ctx)
	upkeep := s.startUpkeep(upkeepCtx)

	defer upkeep.Wait()
	defer stopUpkeep()

	httpServer := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: headerTimeout,
		TLSConfig:         tlsConfig,
	}

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
