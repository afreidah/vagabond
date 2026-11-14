// -------------------------------------------------------------------------------
// Failure Taxonomy
//
// Author: Alex Freidah
//
// Reroute only means something if Vagabond can tell a provider that broke from
// a request it built wrong. A 503 deserves another admitted provider; a 400
// gets the same answer everywhere and is loud instead. A task exiting non-zero
// is not an error here: it is an answer, carried by execution.Result.
// -------------------------------------------------------------------------------

package plugin

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/rest"
)

// -------------------------------------------------------------------------
// CLASSES
// -------------------------------------------------------------------------

// Class is who is at fault for a failed provider operation.
type Class string

// The failure classes a provider operation can produce. Only infrastructure
// may be rerouted. Internal is Vagabond's own fault, kept apart because a
// rerouted bug fails identically everywhere and burns capacity at each stop.
const (
	ClassInfrastructure Class = "infrastructure"
	ClassInternal       Class = "internal"
)

// classNames is the vocabulary Valid and Classes check against.
var classNames = []Class{ClassInfrastructure, ClassInternal}

// Classes returns a copy of the valid failure classes, so a caller cannot
// reorder the vocabulary for everyone else.
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
// different provider. An internal failure reproduces wherever it is sent.
func (c Class) Reroutable() bool {
	return c == ClassInfrastructure
}

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// ErrProvider is the sentinel behind every classified provider failure.
var ErrProvider = errors.New("provider operation failed")

// ErrNotFound marks a 404 from ClassifyHTTP, so a plugin can tell a resource
// that does not exist from a request that failed.
var ErrNotFound = errors.New("not found")

// ErrUnauthorized marks a 401 or 403 from ClassifyHTTP: the platform rejected
// the credential. Dispatch re-resolves the credential and tries once more,
// since one that expired is the common cause.
var ErrUnauthorized = errors.New("unauthorized")

// Error is a failed provider operation, classified.
//
// Retryable is independent of Class: a 429 and a 503 are both infrastructure
// wanting different backoff, and a 400 must never be retried. Provider and Op
// are set by the dispatcher on the way out, since it knows both.
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

// Internal reports that Vagabond is at fault. Never retryable: the same request
// produces the same failure wherever it goes.
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
// provider. An error that is not a *Error is not: nobody has classified it.
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

// ClassifyHTTP turns a provider's HTTP status into a classified failure, so
// every plugin maps statuses the same way.
//
// A 4xx is internal except 408 and 429, which are the provider declining. A
// 5xx, or any unrecognized status, is infrastructure. A plugin whose platform
// uses a status unusually builds an Error directly.
func ClassifyHTTP(status int, retryAfter time.Duration, err error) *Error {
	wrapped := fmt.Errorf("http %d: %w", status, err)

	switch {
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		e := Infrastructure(wrapped)
		e.RetryAfter = retryAfter

		return e

	case status == http.StatusNotFound:
		return Internal(fmt.Errorf("%w: %w", ErrNotFound, wrapped))

	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return Internal(fmt.Errorf("%w: %w", ErrUnauthorized, wrapped))

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

// ClassifyResponse classifies a failed rest call, so every plugin maps them
// the same way. An error already classified, as a refused credential from a
// token source is, passes through; a refusal goes to refused; a call that got
// no answer is infrastructure; and anything else, a request that could not
// be built or an answer that could not be decoded, is internal.
func ClassifyResponse(err error, refused func(r *rest.Refusal) error) error {
	if err == nil {
		return nil
	}

	if classified, ok := errors.AsType[*Error](err); ok {
		return classified
	}

	if r, ok := errors.AsType[*rest.Refusal](err); ok {
		return refused(r)
	}

	if errors.Is(err, rest.ErrUnreachable) {
		return Infrastructure(err)
	}

	return Internal(err)
}
