// -------------------------------------------------------------------------------
// Duration - time spans written in a job file
//
// Author: Alex Freidah
//
// Holds the text a job file carries, such as "15m", rather than a parsed
// int64. That is not the first choice: Chunk 1 declared this as a
// time.Duration, and recorded an objection that a typed duration loses the HCL
// source range when a value parses but is nonsensical.
//
// The objection turned out to understate the cost. Upstream gohcl decodes
// through gocty, which converts by reflected kind and consults neither
// encoding.TextUnmarshaler nor any custom decoder, so an int64-kinded Duration
// simply rejects "15m" with "a number is required". Nomad solves this with
// RegisterExpressionDecoder on gohcl.Decoder, which is an addition in their
// fork of HCL rather than something upstream offers.
//
// So the value stays text until validation proves it parses, which is also the
// honest description of what is in the file.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"time"
)

// Duration is a time span written in a job file, such as "15m" or "5s".
type Duration string

// ErrInvalidDuration is the sentinel behind every unparseable duration.
var ErrInvalidDuration = errors.New("invalid duration")

// FromDuration renders a standard library duration as a job file would write
// it, for constructing a specification in Go rather than reading one.
func FromDuration(d time.Duration) Duration {
	return Duration(d.String())
}

// Std parses the value into a standard library duration.
//
// Returns an error rather than a zero, because a zero timeout and an
// unparseable one mean opposite things and silently conflating them is how a
// task ends up with no bound at all. Validation checks this before anything
// schedules against it, so a caller past that point is handling an error that
// cannot happen, which is the right cost for not being able to produce a wrong
// answer.
func (d Duration) Std() (time.Duration, error) {
	parsed, err := time.ParseDuration(string(d))
	if err != nil {
		return 0, fmt.Errorf("%w %q: %w", ErrInvalidDuration, string(d), err)
	}

	if parsed < 0 {
		return 0, fmt.Errorf("%w %q: must not be negative", ErrInvalidDuration, string(d))
	}

	return parsed, nil
}

// Valid reports whether the value parses as a non-negative duration.
func (d Duration) Valid() bool {
	_, err := d.Std()

	return err == nil
}

// String returns the duration as written.
func (d Duration) String() string {
	return string(d)
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	if !d.Valid() {
		return nil, fmt.Errorf("%w %q", ErrInvalidDuration, string(d))
	}

	return []byte(d), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
//
// Validates, so that an unparseable duration cannot enter the model through
// JSON or a database column. HCL is the exception: gohcl assigns the string
// directly without consulting this, which is why validation checks durations
// explicitly rather than trusting decoding to have done it.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed := Duration(text)
	if !parsed.Valid() {
		return fmt.Errorf("%w %q", ErrInvalidDuration, string(text))
	}

	*d = parsed

	return nil
}
