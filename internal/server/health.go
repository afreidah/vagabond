// -------------------------------------------------------------------------------
// Health Route
//
// Author: Alex Freidah
//
// Whether the server can take new work. A server whose store is unreachable
// still answers plans from its last snapshot and finishes what it is running,
// but refuses new dispatches, so it reports 503 for a load balancer to act on.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"net/http"
	"time"

	"github.com/afreidah/vagabond/internal/api"
)

// pingTimeout bounds asking the store whether it answers.
const pingTimeout = 5 * time.Second

// health answers 200 when the store answers and 503 when it does not, with
// how old the usage snapshot is and how many dispatches are running.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
	defer cancel()

	out := api.Health{
		Store:          "ok",
		UsageRefreshed: s.ledger.Refreshed().UTC(),
		Running:        len(s.runningIDs()),
	}

	status := http.StatusOK

	if err := s.executions.Ping(ctx); err != nil {
		out.Store, out.StoreError = "unreachable", err.Error()
		status = http.StatusServiceUnavailable
	}

	write(w, status, out)
}
