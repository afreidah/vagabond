// -------------------------------------------------------------------------------
// REPORT Line Tests
//
// Author: Alex Freidah
// -------------------------------------------------------------------------------

package aws

import (
	"testing"
	"time"
)

const reportLine = "REPORT RequestId: 3f1c\tDuration: 12.34 ms\tBilled Duration: 13 ms\t" +
	"Memory Size: 512 MB\tMax Memory Used: 70 MB\tInit Duration: 150.00 ms\t\n"

func TestParseReport(t *testing.T) {
	t.Parallel()

	r, ok := parseReport("START RequestId: 3f1c\nhello\nEND RequestId: 3f1c\n" + reportLine)
	if !ok {
		t.Fatal("parseReport() found no REPORT line")
	}

	// Rounded up, like every other duration the ledger reads.
	if r.duration != 12340*time.Microsecond {
		t.Errorf("duration = %v, want 12.34ms", r.duration)
	}

	if r.billed != 13*time.Millisecond {
		t.Errorf("billed = %v, want 13ms", r.billed)
	}

	if r.memory != 512 {
		t.Errorf("memory = %d, want 512", r.memory)
	}

	if !r.hasBill() {
		t.Error("hasBill() = false for a complete line")
	}
}

// Init Duration and Billed Duration both contain "Duration:", and neither may
// be read as the run's own duration.
func TestParseReport_DurationIsTheRunsOwn(t *testing.T) {
	t.Parallel()

	r, _ := parseReport("REPORT RequestId: x\tInit Duration: 900.00 ms\tBilled Duration: 5 ms\tDuration: 4.10 ms\tMemory Size: 128 MB")

	if r.duration != 4100*time.Microsecond {
		t.Errorf("duration = %v, want 4.1ms", r.duration)
	}
}

// A tail cut short can lose the line, and then there is no bill to report.
func TestParseReport_Missing(t *testing.T) {
	t.Parallel()

	if _, ok := parseReport("some output\nEND RequestId: x\n"); ok {
		t.Error("parseReport() found a REPORT line in a tail without one")
	}

	r, ok := parseReport("REPORT RequestId: x\tDuration: 4.10 ms\t")
	if !ok || r.hasBill() {
		t.Errorf("a line without Billed Duration or Memory Size reported a bill: %+v", r)
	}
}
