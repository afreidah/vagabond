// -------------------------------------------------------------------------------
// Execution ID
//
// Author: Alex Freidah
//
// Vagabond mints the ID and records it before calling Submit. UUIDv7 is
// time-sortable, so it works as a CockroachDB primary key without a hot write
// range and orders listings chronologically. It also embeds its creation time,
// which is why nothing stores a separate SubmittedAt.
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

// ID identifies one dispatched run of a task. The provider's identifier is
// carried alongside it, since that one is unique only within the provider,
// assigned too late for idempotency, and absent if a submission fails.
type ID uuid.UUID

// ErrInvalidID is the sentinel behind every unparseable execution ID.
var ErrInvalidID = errors.New("invalid execution id")

// -------------------------------------------------------------------------
// CONSTRUCTOR
// -------------------------------------------------------------------------

// NewID mints a time-sortable identifier for a new execution. It fails only
// when the system entropy source does, which is why no test covers it.
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

// IsZero reports whether the ID was never set. The zero ID is not a valid
// execution.
func (id ID) IsZero() bool {
	return id == ID{}
}

// Created returns when the ID was minted, read from the UUIDv7 timestamp: when
// Vagabond decided to dispatch, not when the provider accepted the work.
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
