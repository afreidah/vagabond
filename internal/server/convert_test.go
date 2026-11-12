// -------------------------------------------------------------------------------
// API Conversion Tests
//
// Author: Alex Freidah
//
// The result fields of an execution's API view, which the CLI prints its
// verdict from.
// -------------------------------------------------------------------------------

package server

import (
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/quota"
)

// A recorded result reaches the API view whole, the platform's reason included.
func TestExecutionOf_CarriesTheResult(t *testing.T) {
	t.Parallel()

	id, err := execution.NewID()
	if err != nil {
		t.Fatalf("NewID() = %v", err)
	}

	const reason = "Evicted: Pod ephemeral local storage usage exceeds the total limit of containers 400M."

	got := executionOf(&execution.Record{
		ID:    id,
		State: execution.StateFailed,
		Result: &execution.Result{
			ID:            id,
			ExitCode:      new(137),
			Duration:      35 * time.Second,
			Billed:        &quota.Execution{CPU: 2000, Memory: 3814, Duration: 36 * time.Second},
			LogsTruncated: true,
			Reason:        reason,
		},
	})

	if !got.HasResult || got.ExitCode == nil || *got.ExitCode != 137 || got.Duration != 35*time.Second {
		t.Errorf("HasResult %t, exit %v, duration %s; want true, 137, 35s", got.HasResult, got.ExitCode, got.Duration)
	}

	if got.Billed == nil || *got.Billed != (api.Billed{CPU: 2000, Memory: 3814, Duration: 36 * time.Second}) {
		t.Errorf("Billed = %+v", got.Billed)
	}

	if !got.LogsTruncated || got.Reason != reason {
		t.Errorf("LogsTruncated %t, Reason %q; want true, %q", got.LogsTruncated, got.Reason, reason)
	}
}
