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

// DriverName is the execution contract a task requests.
//
// It is a distinct type rather than a bare string so that a driver cannot be
// passed where a provider name is expected. The two are routinely adjacent in
// admission and scheduling code and read identically at a call site, which is
// the confusion this type exists to prevent.
type DriverName string

// The drivers Vagabond implements, and the execution contract each one names.
//
// DriverContainer runs an arbitrary container image to completion and takes its
// exit status as the result, imposing no runtime contract on the image beyond
// exiting. DriverFunction invokes a handler that satisfies the target
// platform's runtime contract, which a function-packaged container image must
// still implement even though it is a container. DriverWorker invokes a
// predeployed constrained executor, typically an edge or Wasm runtime, and can
// request only the operations that executor already implements.
//
// The vocabulary is closed: a provider advertises which of these it satisfies
// and cannot introduce its own.
const (
	DriverContainer DriverName = "container"
	DriverFunction  DriverName = "function"
	DriverWorker    DriverName = "worker"
)

// driverNames is the canonical vocabulary, and the single source of truth for
// both validation and the list an error message prints. Declaration order is
// the order users see.
var driverNames = []DriverName{DriverContainer, DriverFunction, DriverWorker}

// Drivers returns the valid driver names in declaration order.
//
// The returned slice is a copy, so a caller rendering it into help text or an
// error cannot reorder or truncate the vocabulary for everyone else.
func Drivers() []DriverName {
	return slices.Clone(driverNames)
}

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

// Valid reports whether d is a driver Vagabond implements.
//
// Comparison is exact. Driver names are lowercase and case-sensitive, so
// "Container" is not accepted. A closed vocabulary that quietly normalizes
// spelling ends up with two renderings of the same driver across job files and
// persisted rows, and the second one surfaces long after the error would have.
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

// UnknownDriverError reports a driver name Vagabond does not implement.
//
// Name is preserved exactly as it was written rather than normalized, because
// the whole value of this error is showing an author the string they typed next
// to the strings they could have typed.
type UnknownDriverError struct {
	Name string
}

// Error renders the rejected name alongside the full valid vocabulary. The
// vocabulary is short and the correction is almost always a spelling the reader
// can take directly off the list.
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

// MarshalText implements encoding.TextMarshaler.
//
// Marshalling validates, which is deliberate. The zero DriverName is not a
// driver, and a closed vocabulary that allows an unset value into an API
// response or a database column has only deferred the failure to whatever reads
// it back, where the context that would explain it is gone.
//
// Note that this type deliberately implements no database interfaces. Those
// would require importing a driver, which belongs only in internal/state; a
// text encoding is enough for persistence to build on.
func (d DriverName) MarshalText() ([]byte, error) {
	if !d.Valid() {
		return nil, &UnknownDriverError{Name: string(d)}
	}

	return []byte(d), nil
}

// UnmarshalText implements encoding.TextUnmarshaler, rejecting any name outside
// the vocabulary so that an invalid driver cannot enter the model by way of
// JSON or an HCL string value.
func (d *DriverName) UnmarshalText(text []byte) error {
	parsed, err := ParseDriverName(string(text))
	if err != nil {
		return err
	}

	*d = parsed

	return nil
}
