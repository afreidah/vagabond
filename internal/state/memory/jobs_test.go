// -------------------------------------------------------------------------------
// Memory Job Store Tests
//
// Author: Alex Freidah
//
// The memory store follows the same registered job rules as Postgres, periodic
// launches included.
// -------------------------------------------------------------------------------

package memory

import (
	"testing"

	"github.com/afreidah/vagabond/internal/state/statetest"
)

// The memory store keeps the registered job rules every store keeps.
func TestJobs_Contract(t *testing.T) {
	statetest.JobContract(t, func(*testing.T) statetest.JobStore { return NewJobs() })
}
