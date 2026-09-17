// -------------------------------------------------------------------------------
// Duration - time spans written in a job file
//
// Author: Alex Freidah
//
// Wraps time.Duration so that the text form is part of the type. Admission
// compares a requested duration against provider limits on every candidate,
// and a representation that stayed a string would mean parsing at each
// comparison site.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"time"
)

// Duration is a time span written in a job file, such as "15m" or "5s".
type Duration time.Duration

// ErrInvalidDuration is the sentinel behind every unparseable duration.
var ErrInvalidDuration = errors.New("invalid duration")

// ParseDuration converts s into a Duration.
//
// Negative values are rejected. A negative timeout or backoff has no meaning
// Vagabond could act on, and accepting one defers the failure to whichever
// provider plugin eventually divides by it.
func ParseDuration(s string) (Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%w %q: %w", ErrInvalidDuration, s, err)
	}

	if d < 0 {
		return 0, fmt.Errorf("%w %q: must not be negative", ErrInvalidDuration, s)
	}

	return Duration(d), nil
}

// Std returns the value as a standard library duration for arithmetic and
// comparison.
func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

// String renders the duration in the form a job file would use.
func (d Duration) String() string {
	return time.Duration(d).String()
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := ParseDuration(string(text))
	if err != nil {
		return err
	}

	*d = parsed

	return nil
}
