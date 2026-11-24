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

import (
	"slices"
	"strings"
	"testing"
)

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

// A read skips what of its first chunk precedes its offset, and is placed
// past output dropped before the chunk it reaches.
func TestPlace(t *testing.T) {
	type chunk struct {
		seq  int64
		data string
	}

	tests := []struct {
		name   string
		offset int64
		chunks []chunk
		want   string
		starts []int64
	}{
		{name: "from the start", offset: 0, chunks: []chunk{{0, "abc"}}, want: "abc"},
		{name: "inside a chunk", offset: 1, chunks: []chunk{{0, "abc"}}, want: "bc"},
		{name: "at the end of a chunk", offset: 3, chunks: []chunk{{0, "abc"}}, want: ""},
		{name: "in the next chunk", offset: LogChunk + 1, chunks: []chunk{{1, "xyz"}}, want: "yz"},
		{name: "in dropped output", offset: 5, chunks: []chunk{{2, "kept"}}, want: "kept", starts: []int64{2 * LogChunk}},
		{
			name: "past a gap", offset: 0, chunks: []chunk{{0, "a"}, {3, "d"}},
			want: "ad", starts: []int64{3 * LogChunk},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := &placed{}
			each := Place(tc.offset, out)

			for _, c := range tc.chunks {
				if err := each(c.seq, []byte(c.data)); err != nil {
					t.Fatalf("chunk %d: %v", c.seq, err)
				}
			}

			if out.String() != tc.want || !slices.Equal(out.starts, tc.starts) {
				t.Errorf("wrote %q placed at %v, want %q placed at %v", out.String(), out.starts, tc.want, tc.starts)
			}
		})
	}
}

// placed is a LogWriter recording what it was written and where it was placed.
type placed struct {
	strings.Builder
	starts []int64
}

// StartsAt records offset.
func (p *placed) StartsAt(offset int64) error {
	p.starts = append(p.starts, offset)

	return nil
}
