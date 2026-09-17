// -------------------------------------------------------------------------------
// Execution ID Tests
//
// Author: Alex Freidah
//
// The sortability and embedded timestamp are not incidental: one makes the ID a
// usable CockroachDB primary key, the other is why nothing stores a separate
// SubmittedAt. Both are asserted here so a change of scheme cannot pass
// quietly.
// -------------------------------------------------------------------------------

package execution

import (
	"errors"
	"testing"
	"time"
)

func TestNewID_IsUnique(t *testing.T) {
	seen := make(map[string]bool, 1000)

	for range 1000 {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID() returned unexpected error: %v", err)
		}

		if seen[id.String()] {
			t.Fatalf("NewID() produced a duplicate: %s", id)
		}

		seen[id.String()] = true
	}
}

// UUIDv7 is time-ordered, which is what keeps it from being a hot write range
// as a primary key and what orders listings for free.
func TestNewID_IsTimeSortable(t *testing.T) {
	first, err := NewID()
	if err != nil {
		t.Fatalf("NewID() returned unexpected error: %v", err)
	}

	time.Sleep(2 * time.Millisecond)

	second, err := NewID()
	if err != nil {
		t.Fatalf("NewID() returned unexpected error: %v", err)
	}

	if first.String() >= second.String() {
		t.Errorf("ids are not lexically ordered by time: %s then %s", first, second)
	}
}

// The embedded timestamp is why no SubmittedAt field exists.
func TestID_Created(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID() returned unexpected error: %v", err)
	}

	after := time.Now().UTC().Add(time.Second)
	created := id.Created()

	if created.Before(before) || created.After(after) {
		t.Errorf("Created() = %v, want between %v and %v", created, before, after)
	}
}

func TestID_IsZero(t *testing.T) {
	var zero ID
	if !zero.IsZero() {
		t.Error("the zero ID does not report as zero")
	}

	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID() returned unexpected error: %v", err)
	}

	if id.IsZero() {
		t.Error("a minted ID reports as zero")
	}
}

func TestID_TextRoundTrip(t *testing.T) {
	original, err := NewID()
	if err != nil {
		t.Fatalf("NewID() returned unexpected error: %v", err)
	}

	encoded, err := original.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() returned unexpected error: %v", err)
	}

	var got ID
	if err := got.UnmarshalText(encoded); err != nil {
		t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", encoded, err)
	}

	if got != original {
		t.Errorf("round trip produced %s, want %s", got, original)
	}
}

// The zero ID is not an execution. Writing one to a column or naming one in a
// token defers the failure to whatever reads it back.
func TestID_MarshalTextRejectsZeroValue(t *testing.T) {
	var zero ID

	if _, err := zero.MarshalText(); !errors.Is(err, ErrInvalidID) {
		t.Errorf("MarshalText() on the zero ID returned %v, want ErrInvalidID", err)
	}
}

func TestParseID_Invalid(t *testing.T) {
	for _, input := range []string{"", "not-a-uuid", "12345"} {
		t.Run(input, func(t *testing.T) {
			got, err := ParseID(input)
			if err == nil {
				t.Fatalf("ParseID(%q) = %s, want error", input, got)
			}

			if !errors.Is(err, ErrInvalidID) {
				t.Errorf("error does not wrap ErrInvalidID: %v", err)
			}
		})
	}
}

func TestID_UnmarshalTextRejectsInvalid(t *testing.T) {
	var id ID

	err := id.UnmarshalText([]byte("not-a-uuid"))
	if err == nil {
		t.Fatal("UnmarshalText accepted a malformed id")
	}

	if !errors.Is(err, ErrInvalidID) {
		t.Errorf("error does not wrap ErrInvalidID: %v", err)
	}

	if !id.IsZero() {
		t.Error("a refused unmarshal left a value behind")
	}
}
