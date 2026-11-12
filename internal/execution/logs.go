// -------------------------------------------------------------------------------
// Stored Output
//
// Author: Alex Freidah
//
// How an execution's output is laid out in storage. It is kept in fixed-size
// chunks numbered from the start of the output, so a byte offset names its
// chunk and a read from any offset needs no index. Past the ceiling the oldest
// chunks are dropped, since the end of a build is where it failed; positions
// still count from the first byte the task printed.
// -------------------------------------------------------------------------------

package execution

import "time"

// LogChunk is how much output one stored chunk holds. Chunk k holds bytes
// k*LogChunk up to (k+1)*LogChunk of the whole output.
const LogChunk = 256 << 10

// How much of an execution's output is kept, and for how long after it ends,
// when the server sets neither.
const (
	DefaultMaxLogBytes  = 16 << 20
	DefaultLogRetention = 7 * 24 * time.Hour
)

// KeepFrom returns the first chunk to keep of output end bytes long, so that
// what is kept is at most limit bytes. Whole chunks are dropped, so as little
// as limit-LogChunk may be kept.
func KeepFrom(end, limit int64) int64 {
	if end <= limit {
		return 0
	}

	return (end - limit + LogChunk - 1) / LogChunk
}

// PlaceLogs returns the chunk a read from byte offset begins in, and how many
// of its bytes to skip. An offset in output already dropped, before start,
// begins at start.
func PlaceLogs(start, offset int64) (seq, skip int64) {
	offset = max(offset, start)

	return offset / LogChunk, offset % LogChunk
}
