// -------------------------------------------------------------------------------
// Exit Code Tests
//
// Author: Alex Freidah
// -------------------------------------------------------------------------------

package cli

import "testing"

// The documented codes reach the shell as they are; anything else the library
// returns, such as 127 for an unknown command, is a plain failure.
func TestNormalizeExit(t *testing.T) {
	for in, want := range map[int]int{
		ExitSuccess:    ExitSuccess,
		ExitFailure:    ExitFailure,
		ExitNoCapacity: ExitNoCapacity,
		127:            ExitFailure,
		-1:             ExitFailure,
	} {
		if got := normalizeExit(in); got != want {
			t.Errorf("normalizeExit(%d) = %d, want %d", in, got, want)
		}
	}
}
