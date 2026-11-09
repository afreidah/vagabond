// -------------------------------------------------------------------------------
// Duration - time spans written in a job file
//
// Author: Alex Freidah
//
// Holds the text a job file carries, such as "15m", rather than a parsed
// int64. Upstream gohcl converts by reflected kind and consults no
// encoding.TextUnmarshaler, so an int64-kinded Duration would reject "15m". The
// value stays text until validation proves it parses.
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

// Std parses the value into a standard library duration. It returns an error
// rather than zero, since a zero timeout and an unparseable one differ.
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

// UnmarshalText implements encoding.TextUnmarshaler and validates. gohcl
// assigns the string without calling this, so validation checks it itself.
func (d *Duration) UnmarshalText(text []byte) error {
	parsed := Duration(text)
	if !parsed.Valid() {
		return fmt.Errorf("%w %q", ErrInvalidDuration, string(text))
	}

	*d = parsed

	return nil
}
