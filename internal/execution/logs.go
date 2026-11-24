// -------------------------------------------------------------------------------
// Stored Output
//
// Author: Alex Freidah
//
// How an execution's output is laid out in storage. It is stored as it arrives,
// while the execution runs, in fixed-size chunks numbered from the start of the
// output, so a byte offset names its chunk and a read from any offset needs no
// index. Only the last chunk grows. Past the ceiling the oldest chunks are
// dropped, since the end of a build is where it failed; positions still count
// from the first byte the task printed, and a read of output whose front was
// dropped says where what it writes begins.
// -------------------------------------------------------------------------------

package execution

import (
	"io"
	"time"
)

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

// LogWriter is where output read from a byte offset is written. A reader whose
// output continues past where its last write ended, as the bytes between were
// dropped, first calls StartsAt with where what it writes next begins.
type LogWriter interface {
	io.Writer
	StartsAt(offset int64) error
}

// PlacedWriter is a LogWriter over any writer, recording where the output
// written to it last began.
type PlacedWriter struct {
	io.Writer
	Start int64
}

// StartsAt records offset as where the output written next begins. It never
// fails.
func (p *PlacedWriter) StartsAt(offset int64) error {
	p.Start = offset

	return nil
}

// Log is an execution's stored output as a capture writes it while the
// execution runs. A write appends, and Len is where the next one begins,
// counted from the first byte the task printed, so a capture taken over from
// another server resumes there. WriteTo writes what the log holds so far.
type Log interface {
	LogWriter
	io.WriterTo
	Len() int64
}

// Place returns what each stored chunk of a read from byte offset goes through
// on its way to w, the chunks in order from offset's on. The part of a chunk
// before offset is skipped, and a chunk that begins past where the last ended,
// the output between dropped, is first placed through w.StartsAt.
func Place(offset int64, w LogWriter) func(seq int64, data []byte) error {
	next := offset

	return func(seq int64, data []byte) error {
		start := seq * LogChunk

		// Output dropped before this chunk is not there to write
		if start > next {
			if err := w.StartsAt(start); err != nil {
				return err
			}

			next = start
		}

		skip := min(next-start, int64(len(data)))
		n, err := w.Write(data[skip:])
		next += int64(n)

		return err
	}
}
