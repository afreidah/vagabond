// -------------------------------------------------------------------------------
// Platform Timestamps
//
// Author: Alex Freidah
//
// Cloud APIs report when a run started and ended as RFC 3339 strings, absent
// until the run gets there. Reading them one way keeps a missing time zero in
// every provider, which execution.Elapsed refuses to turn into a duration.
// -------------------------------------------------------------------------------

package plugin

import "time"

// ParseTime reads an RFC 3339 timestamp a platform reported. An absent or
// unparseable one is the zero time, since platforms omit what a run has not
// reached.
func ParseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}

	return parsed
}
