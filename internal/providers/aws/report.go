// -------------------------------------------------------------------------------
// The REPORT Line
//
// Author: Alex Freidah
//
// Lambda ends every invocation's log with one line stating what it ran for and
// what it billed:
//
//	REPORT RequestId: <id>	Duration: 12.34 ms	Billed Duration: 13 ms	Memory Size: 128 MB	...
//
// Memory Size is what the function is configured with, which is what AWS
// charges for, not what the task asked for.
// -------------------------------------------------------------------------------

package aws

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reportDuration = regexp.MustCompile(`\tDuration: ([0-9.]+) ms`)
	reportBilled   = regexp.MustCompile(`\tBilled Duration: ([0-9]+) ms`)
	reportMemory   = regexp.MustCompile(`\tMemory Size: ([0-9]+) MB`)
)

// report is what the REPORT line states. A field it lacked is zero.
type report struct {
	duration time.Duration
	billed   time.Duration
	memory   int // MB, which Lambda's GB-second pricing divides by 1024
}

// parseReport finds the REPORT line in a log tail. False when there is none,
// which a tail cut short can cause.
func parseReport(logs string) (report, bool) {
	var line string

	for candidate := range strings.SplitSeq(logs, "\n") {
		if strings.HasPrefix(candidate, "REPORT ") {
			line = candidate
		}
	}

	if line == "" {
		return report{}, false
	}

	var r report

	if m := reportDuration.FindStringSubmatch(line); m != nil {
		if ms, err := strconv.ParseFloat(m[1], 64); err == nil {
			r.duration = time.Duration(math.Ceil(ms * float64(time.Millisecond)))
		}
	}

	if m := reportBilled.FindStringSubmatch(line); m != nil {
		if ms, err := strconv.Atoi(m[1]); err == nil {
			r.billed = time.Duration(ms) * time.Millisecond
		}
	}

	if m := reportMemory.FindStringSubmatch(line); m != nil {
		if mb, err := strconv.Atoi(m[1]); err == nil {
			r.memory = mb
		}
	}

	return r, true
}

// hasBill reports whether the line stated both halves of the charge.
func (r report) hasBill() bool {
	return r.billed > 0 && r.memory > 0
}
