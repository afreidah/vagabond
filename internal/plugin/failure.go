// -------------------------------------------------------------------------------
// Failure Taxonomy
//
// Author: Alex Freidah
//
// Reroute only means something if Vagabond can tell "the provider broke" from
// "we built a bad request". A 503 from IBM deserves another admitted provider;
// a 400 caused by Vagabond sending nonsense will get the same answer everywhere
// and should be loud instead.
//
// Note what is absent. A task exiting non-zero is not an error here: it is a
// real answer, carried by execution.Result. Keeping it off the error path means
// a non-nil error always says something went wrong with Vagabond or a provider,
// never that someone's build is broken.
// -------------------------------------------------------------------------------

package plugin

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// -------------------------------------------------------------------------
// CLASSES
// -------------------------------------------------------------------------

// Class is who is at fault for a failed provider operation.
type Class string

// The failure classes a provider operation can produce.
//
// ClassInfrastructure is the provider failing to give an answer, and is the
// only class that may be rerouted. ClassInternal is Vagabond's own fault: a
// malformed request, a bad bootstrap injection, a task admitted to a provider
// that could never have run it.
//
// The third class earns its place because misattributing our own bug to a
// provider is the worst available outcome. It would be rerouted, fail
// identically everywhere, burn free-tier capacity at each stop, and never
// self-correct.
const (
	ClassInfrastructure Class = "infrastructure"
	ClassInternal       Class = "internal"
)

var classNames = []Class{ClassInfrastructure, ClassInternal}

// Classes returns the valid failure classes.
//
// The returned slice is a copy, so a caller rendering it cannot reorder the
// vocabulary for everyone else.
func Classes() []Class {
	return slices.Clone(classNames)
}

// Valid reports whether c is a class Vagabond records.
func (c Class) Valid() bool {
	return slices.Contains(classNames, c)
}

// String returns the class as it is persisted and reported.
func (c Class) String() string {
	return string(c)
}

// Reroutable reports whether a failure of this class may be retried on a
// different provider.
//
// Only infrastructure failures qualify. An internal failure will reproduce
// wherever it is sent, so rerouting one spends free-tier capacity to reach the
// same answer again.
func (c Class) Reroutable() bool {
	return c == ClassInfrastructure
}

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// ErrProvider is the sentinel behind every classified provider failure.
var ErrProvider = errors.New("provider operation failed")

// Error is a failed provider operation, classified.
//
// Retryable is independent of Class rather than derived from it. A 429 and a
// 503 are both infrastructure and want different backoff, and a 400 is
// infrastructure-shaped but must never be retried at all. Collapsing the two
// into one field loses exactly the distinction that decides what to do next.
//
// Provider and Op are set by the dispatcher on the way out rather than by each
// plugin. The dispatcher knows which provider it called and what it asked for,
// and leaving it to every plugin means it is eventually forgotten in one.
type Error struct {
	Class      Class
	Retryable  bool
	RetryAfter time.Duration
	Provider   string
	Op         string
	Err        error
}

// Error renders the classification, the operation, and the underlying cause.
func (e *Error) Error() string {
	var b strings.Builder

	b.WriteString(string(e.Class))
	b.WriteString(" failure")

	if e.Provider != "" {
		b.WriteString(" from ")
		b.WriteString(e.Provider)
	}

	if e.Op != "" {
		b.WriteString(" during ")
		b.WriteString(e.Op)
	}

	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}

	return b.String()
}

// Unwrap returns the underlying cause so that errors.Is and errors.As reach
// whatever the provider SDK returned.
func (e *Error) Unwrap() error {
	return e.Err
}

// Is reports a match against ErrProvider, so a caller that only needs to know a
// provider operation failed does not have to know this type exists.
func (e *Error) Is(target error) bool {
	return target == ErrProvider
}

// Reroutable reports whether this failure may be retried on another provider.
func (e *Error) Reroutable() bool {
	return e.Class.Reroutable()
}

// -------------------------------------------------------------------------
// CONSTRUCTORS
// -------------------------------------------------------------------------

// Infrastructure reports that a provider failed to give an answer.
func Infrastructure(err error) *Error {
	return &Error{Class: ClassInfrastructure, Retryable: true, Err: err}
}

// Internal reports that Vagabond is at fault.
//
// Never retryable: the same request produces the same failure wherever it goes.
func Internal(err error) *Error {
	return &Error{Class: ClassInternal, Retryable: false, Err: err}
}

// WithContext fills in the provider and operation, and is what the dispatcher
// calls on the way out.
func (e *Error) WithContext(provider, op string) *Error {
	e.Provider = provider
	e.Op = op

	return e
}

// Reroutable reports whether an arbitrary error may be retried on another
// provider.
//
// Here rather than in each caller, because every consumer of the Provider
// interface has to ask this, and a handful of separate errors.As calls is how
// the two classes start being judged differently in different places.
//
// An error that is not a *Error is not reroutable. An unclassified failure is
// one nobody has reasoned about, and sending work onward on that basis spends
// capacity on a guess.
func Reroutable(err error) bool {
	var classified *Error
	if !errors.As(err, &classified) {
		return false
	}

	return classified.Reroutable()
}

// -------------------------------------------------------------------------
// HTTP CLASSIFICATION
// -------------------------------------------------------------------------

// ClassifyHTTP turns a provider's HTTP status into a classified failure.
//
// Every provider plugin faces the same question against a different SDK, and
// three of them written months apart will answer it inconsistently if each is
// left to judgment. The mapping here is the default; a plugin with a status its
// platform uses unusually builds an Error directly instead.
//
// A 4xx means Vagabond sent something wrong and is internal, with two
// exceptions that are the provider declining rather than objecting: 408 and
// 429. A 5xx is the provider failing. Anything else is treated as
// infrastructure, since an unrecognized status is not evidence of our own bug.
func ClassifyHTTP(status int, retryAfter time.Duration, err error) *Error {
	wrapped := fmt.Errorf("http %d: %w", status, err)

	switch {
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		e := Infrastructure(wrapped)
		e.RetryAfter = retryAfter

		return e

	case status >= 400 && status < 500:
		return Internal(wrapped)

	case status >= 500:
		e := Infrastructure(wrapped)
		e.RetryAfter = retryAfter

		return e

	default:
		return Infrastructure(wrapped)
	}
}
