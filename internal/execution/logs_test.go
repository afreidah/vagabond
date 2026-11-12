// -------------------------------------------------------------------------------
// Stored Output Tests
//
// Author: Alex Freidah
//
// The layout is arithmetic at chunk boundaries, so each rule is driven on both
// sides of one, where an off-by-one would put a read in the wrong chunk or keep
// a byte past the ceiling.
// -------------------------------------------------------------------------------

package execution

import "testing"

// What is kept never exceeds the limit, and is cut only at chunk boundaries.
func TestKeepFrom(t *testing.T) {
	tests := []struct {
		name       string
		end, limit int64
		want       int64
	}{
		{name: "empty", end: 0, limit: 4 * LogChunk, want: 0},
		{name: "under the limit", end: LogChunk, limit: 4 * LogChunk, want: 0},
		{name: "exactly the limit", end: 4 * LogChunk, limit: 4 * LogChunk, want: 0},
		{name: "one byte over", end: 4*LogChunk + 1, limit: 4 * LogChunk, want: 1},
		{name: "a chunk over", end: 5 * LogChunk, limit: 4 * LogChunk, want: 1},
		{name: "a chunk and a byte over", end: 5*LogChunk + 1, limit: 4 * LogChunk, want: 2},
		{name: "a limit inside a chunk", end: 3 * LogChunk, limit: LogChunk / 2, want: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := KeepFrom(tc.end, tc.limit)
			if got != tc.want {
				t.Errorf("KeepFrom(%d, %d) = %d, want %d", tc.end, tc.limit, got, tc.want)
			}

			if kept := tc.end - got*LogChunk; kept > tc.limit {
				t.Errorf("keeps %d bytes, over the limit of %d", kept, tc.limit)
			}
		})
	}
}

// A read begins in the chunk holding its offset, or at the first byte kept
// when its offset was dropped.
func TestPlaceLogs(t *testing.T) {
	tests := []struct {
		name          string
		start, offset int64
		seq, skip     int64
	}{
		{name: "from the start", start: 0, offset: 0, seq: 0, skip: 0},
		{name: "inside the first chunk", start: 0, offset: 10, seq: 0, skip: 10},
		{name: "at a boundary", start: 0, offset: LogChunk, seq: 1, skip: 0},
		{name: "past a boundary", start: 0, offset: LogChunk + 3, seq: 1, skip: 3},
		{name: "in dropped output", start: 2 * LogChunk, offset: 5, seq: 2, skip: 0},
		{name: "past the dropped output", start: 2 * LogChunk, offset: 2*LogChunk + 7, seq: 2, skip: 7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seq, skip := PlaceLogs(tc.start, tc.offset)
			if seq != tc.seq || skip != tc.skip {
				t.Errorf("PlaceLogs(%d, %d) = %d, %d; want %d, %d",
					tc.start, tc.offset, seq, skip, tc.seq, tc.skip)
			}
		})
	}
}
