// -------------------------------------------------------------------------------
// Execution ID
//
// Author: Alex Freidah
//
// Vagabond mints the ID and records it before calling Submit, so a crash
// between dispatching and recording leaves a row to reconcile against instead
// of an orphaned run on someone else's free tier. The same ID is the
// submission idempotency key and the subject of the token the bootstrap
// presents when it reports a result.
//
// UUIDv7 is time-sortable, so it works as a CockroachDB primary key without a
// hot write range and orders listings chronologically for free. It also embeds
// its creation time, which is why nothing stores a separate SubmittedAt.
// -------------------------------------------------------------------------------

package execution

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// ID identifies one dispatched run of a task.
//
// It is distinct from the provider's own identifier, which is carried alongside
// it rather than substituted for it. A provider identifier is unique only
// within that provider, is assigned too late to be useful for idempotency, and
// is gone entirely if a submission fails before returning.
type ID uuid.UUID

// ErrInvalidID is the sentinel behind every unparseable execution ID.
var ErrInvalidID = errors.New("invalid execution id")

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// NewID mints a time-sortable identifier for a new execution.
//
// The error path is reachable only when the system entropy source fails, which
// is why no test covers it: a process that cannot read random bytes has larger
// problems than an unminted execution id.
func NewID() (ID, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return ID{}, fmt.Errorf("minting execution id: %w", err)
	}

	return ID(u), nil
}

// ParseID converts the canonical string form back into an ID.
func ParseID(s string) (ID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return ID{}, fmt.Errorf("%w %q: %w", ErrInvalidID, s, err)
	}

	return ID(u), nil
}

// -------------------------------------------------------------------------
// QUERIES
// -------------------------------------------------------------------------

// String returns the canonical hyphenated form.
func (id ID) String() string {
	return uuid.UUID(id).String()
}

// IsZero reports whether the ID was never set.
//
// The zero ID is not a valid execution. Writing one to a database column or
// naming one in a token would defer the failure to whatever reads it back.
func (id ID) IsZero() bool {
	return id == ID{}
}

// Created returns the time the ID was minted, read from the UUIDv7 timestamp.
//
// This is when Vagabond decided to dispatch, not when the provider accepted the
// work. The two differ by the length of one API call, and by much more when a
// submission is retried.
func (id ID) Created() time.Time {
	sec, nsec := uuid.UUID(id).Time().UnixTime()

	return time.Unix(sec, nsec).UTC()
}

// -------------------------------------------------------------------------
// TEXT ENCODING
// -------------------------------------------------------------------------

// MarshalText implements encoding.TextMarshaler, rejecting the zero ID so that
// an unset value cannot reach a database column or an API response.
func (id ID) MarshalText() ([]byte, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("%w: the zero id is not an execution", ErrInvalidID)
	}

	return []byte(id.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *ID) UnmarshalText(text []byte) error {
	parsed, err := ParseID(string(text))
	if err != nil {
		return err
	}

	*id = parsed

	return nil
}
