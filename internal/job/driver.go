// -------------------------------------------------------------------------------
// Driver Vocabulary - the execution contracts a task can request
//
// Author: Alex Freidah
//
// A task declares a driver, which names the execution contract it needs rather
// than the cloud expected to satisfy it. The vocabulary is closed, so a job file
// naming a driver Vagabond does not implement fails while it is being read,
// with an error listing what is valid. The alternative is that the name reaches
// admission and is turned away by every provider at once, which reads like a
// capacity problem and is actually a typo.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// -------------------------------------------------------------------------
// DRIVER NAMES
// -------------------------------------------------------------------------

// DriverName is the execution contract a task requests. It is a distinct type
// so a driver cannot be passed where an adjacent provider name is expected.
type DriverName string

// The drivers Vagabond implements. A provider advertises which of these it
// satisfies and cannot introduce its own.
//
// DriverContainer runs an image to completion and takes its exit status as the
// result. DriverFunction invokes a handler satisfying the platform's runtime
// contract, even when it is packaged as a container image. DriverWorker invokes
// a predeployed constrained executor, such as an edge or Wasm runtime.
const (
	DriverContainer DriverName = "container"
	DriverFunction  DriverName = "function"
	DriverWorker    DriverName = "worker"
)

// driverNames is the vocabulary validation checks and error messages print, in
// the order users see.
var driverNames = []DriverName{DriverContainer, DriverFunction, DriverWorker}

// Drivers returns the valid driver names in declaration order, as a copy.
func Drivers() []DriverName {
	return slices.Clone(driverNames)
}

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

// Valid reports whether d is a driver Vagabond implements. Comparison is exact,
// so one spelling of each driver reaches job files and persisted rows.
func (d DriverName) Valid() bool {
	return slices.Contains(driverNames, d)
}

// ParseDriverName converts s into a DriverName, or reports that it is not one.
func ParseDriverName(s string) (DriverName, error) {
	d := DriverName(s)
	if !d.Valid() {
		return "", &UnknownDriverError{Name: s}
	}

	return d, nil
}

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// ErrUnknownDriver is the sentinel behind every unknown-driver failure.
//
// Callers that only need the category match it with errors.Is; callers that
// need the rejected spelling extract an *UnknownDriverError with errors.As.
var ErrUnknownDriver = errors.New("unknown driver")

// UnknownDriverError reports a driver name Vagabond does not implement. Name is
// kept exactly as written, so the error shows it next to the valid spellings.
type UnknownDriverError struct {
	Name string
}

// Error renders the rejected name alongside the full valid vocabulary.
func (e *UnknownDriverError) Error() string {
	valid := make([]string, 0, len(driverNames))
	for _, d := range driverNames {
		valid = append(valid, string(d))
	}

	return fmt.Sprintf("%s %q: valid drivers are %s",
		ErrUnknownDriver, e.Name, strings.Join(valid, ", "))
}

// Unwrap reports ErrUnknownDriver so that errors.Is matches the category
// without callers needing to know this type exists.
func (e *UnknownDriverError) Unwrap() error {
	return ErrUnknownDriver
}

// -------------------------------------------------------------------------
// TEXT ENCODING
// -------------------------------------------------------------------------

// String returns the driver name as it is written in a job file.
func (d DriverName) String() string {
	return string(d)
}

// MarshalText implements encoding.TextMarshaler and validates, so an unset
// driver fails here rather than in whatever reads the stored text back.
func (d DriverName) MarshalText() ([]byte, error) {
	if !d.Valid() {
		return nil, &UnknownDriverError{Name: string(d)}
	}

	return []byte(d), nil
}

// UnmarshalText implements encoding.TextUnmarshaler, so an invalid driver
// cannot enter the model through JSON or an HCL string value.
func (d *DriverName) UnmarshalText(text []byte) error {
	parsed, err := ParseDriverName(string(text))
	if err != nil {
		return err
	}

	*d = parsed

	return nil
}
