// -------------------------------------------------------------------------------
// Driver Vocabulary Tests
//
// Author: Alex Freidah
//
// The vocabulary is closed, so these tests are mostly about what is rejected
// rather than what is accepted. Case variants and surrounding whitespace get
// explicit coverage because they are the spellings a person actually writes,
// and silently accepting either would put two renderings of one driver into job
// files and persisted rows.
// -------------------------------------------------------------------------------

package job

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// -------------------------------------------------------------------------
// VALIDATION
// -------------------------------------------------------------------------

func TestDriverName_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input DriverName
		want  bool
	}{
		{name: "container", input: DriverContainer, want: true},
		{name: "function", input: DriverFunction, want: true},
		{name: "worker", input: DriverWorker, want: true},
		{name: "empty", input: "", want: false},
		{name: "unknown", input: "lambda", want: false},
		{name: "retired oci-job spelling", input: "oci-job", want: false},
		{name: "provider name is not a driver", input: "ibm-code-engine", want: false},
		{name: "title case", input: "Container", want: false},
		{name: "upper case", input: "CONTAINER", want: false},
		{name: "leading space", input: " container", want: false},
		{name: "trailing newline", input: "container\n", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("DriverName(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseDriverName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  DriverName
	}{
		{name: "container", input: "container", want: DriverContainer},
		{name: "function", input: "function", want: DriverFunction},
		{name: "worker", input: "worker", want: DriverWorker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDriverName(tt.input)
			if err != nil {
				t.Fatalf("ParseDriverName(%q) returned unexpected error: %v", tt.input, err)
			}

			if got != tt.want {
				t.Errorf("ParseDriverName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// A rejected name must yield the zero value rather than the string that was
// refused, so that a caller ignoring the error cannot carry an invalid driver
// forward.
func TestParseDriverName_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "unknown", input: "nomad"},
		{name: "case variant", input: "Worker"},
		{name: "retired oci-job spelling", input: "oci-job"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDriverName(tt.input)
			if err == nil {
				t.Fatalf("ParseDriverName(%q) = %q, want error", tt.input, got)
			}

			if got != "" {
				t.Errorf("ParseDriverName(%q) returned %q alongside an error, want zero value",
					tt.input, got)
			}
		})
	}
}

// -------------------------------------------------------------------------
// ERRORS
// -------------------------------------------------------------------------

// A caller branching on the category must not need to know the concrete type,
// and a caller reporting the problem must be able to recover what was written.
func TestParseDriverName_ErrorIsAndAs(t *testing.T) {
	_, err := ParseDriverName("lambda")
	if err == nil {
		t.Fatal("ParseDriverName(\"lambda\") returned no error")
	}

	if !errors.Is(err, ErrUnknownDriver) {
		t.Errorf("errors.Is(err, ErrUnknownDriver) = false, want true")
	}

	var unknown *UnknownDriverError
	if !errors.As(err, &unknown) {
		t.Fatalf("errors.As did not yield *UnknownDriverError, got %T", err)
	}

	if unknown.Name != "lambda" {
		t.Errorf("UnknownDriverError.Name = %q, want %q", unknown.Name, "lambda")
	}
}

// The error reaches a person editing a job file, so it has to carry both what
// they wrote and what they could have written.
func TestUnknownDriverError_ListsValidVocabulary(t *testing.T) {
	err := &UnknownDriverError{Name: "oci-job"}
	msg := err.Error()

	if !strings.Contains(msg, `"oci-job"`) {
		t.Errorf("error message %q does not quote the rejected name", msg)
	}

	for _, d := range Drivers() {
		if !strings.Contains(msg, d.String()) {
			t.Errorf("error message %q omits valid driver %q", msg, d)
		}
	}
}

// -------------------------------------------------------------------------
// VOCABULARY
// -------------------------------------------------------------------------

// Drivers hands out a copy so that a caller rendering it into help text cannot
// reorder or truncate the vocabulary for every later caller.
func TestDrivers_ReturnsCopy(t *testing.T) {
	first := Drivers()
	if len(first) == 0 {
		t.Fatal("Drivers() returned an empty vocabulary")
	}

	first[0] = "mutated"

	second := Drivers()
	if second[0] == "mutated" {
		t.Error("Drivers() exposed the package-level vocabulary to mutation")
	}
}

// Every constant must be in the vocabulary, and every vocabulary entry must
// validate. This catches a driver added to one and not the other.
func TestDrivers_MatchesConstants(t *testing.T) {
	constants := []DriverName{DriverContainer, DriverFunction, DriverWorker}

	got := Drivers()
	if len(got) != len(constants) {
		t.Fatalf("Drivers() returned %d entries, want %d", len(got), len(constants))
	}

	for i, want := range constants {
		if got[i] != want {
			t.Errorf("Drivers()[%d] = %q, want %q", i, got[i], want)
		}
	}

	for _, d := range got {
		if !d.Valid() {
			t.Errorf("Drivers() contains %q, which does not validate", d)
		}
	}
}

// -------------------------------------------------------------------------
// TEXT ENCODING
// -------------------------------------------------------------------------

func TestDriverName_MarshalText(t *testing.T) {
	tests := []struct {
		name    string
		input   DriverName
		want    string
		wantErr bool
	}{
		{name: "container", input: DriverContainer, want: "container"},
		{name: "function", input: DriverFunction, want: "function"},
		{name: "worker", input: DriverWorker, want: "worker"},
		{name: "zero value is not a driver", input: "", wantErr: true},
		{name: "unknown", input: "lambda", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.input.MarshalText()

			if tt.wantErr {
				if err == nil {
					t.Fatalf("DriverName(%q).MarshalText() = %q, want error", tt.input, got)
				}

				return
			}

			if err != nil {
				t.Fatalf("DriverName(%q).MarshalText() returned unexpected error: %v", tt.input, err)
			}

			if string(got) != tt.want {
				t.Errorf("DriverName(%q).MarshalText() = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDriverName_UnmarshalText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  DriverName
	}{
		{name: "container", input: "container", want: DriverContainer},
		{name: "function", input: "function", want: DriverFunction},
		{name: "worker", input: "worker", want: DriverWorker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got DriverName
			if err := got.UnmarshalText([]byte(tt.input)); err != nil {
				t.Fatalf("UnmarshalText(%q) returned unexpected error: %v", tt.input, err)
			}

			if got != tt.want {
				t.Errorf("UnmarshalText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDriverName_UnmarshalTextInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "unknown", input: "fargate"},
		{name: "case variant", input: "Function"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got DriverName

			err := got.UnmarshalText([]byte(tt.input))
			if err == nil {
				t.Fatalf("UnmarshalText(%q) = %q, want error", tt.input, got)
			}

			if !errors.Is(err, ErrUnknownDriver) {
				t.Errorf("UnmarshalText(%q) error does not wrap ErrUnknownDriver: %v", tt.input, err)
			}
		})
	}
}

// The type crosses the API boundary and a database column as text, so a value
// that goes out must come back identical.
func TestDriverName_JSONRoundTrip(t *testing.T) {
	type task struct {
		Driver DriverName `json:"driver"`
	}

	for _, want := range Drivers() {
		t.Run(want.String(), func(t *testing.T) {
			encoded, err := json.Marshal(task{Driver: want})
			if err != nil {
				t.Fatalf("json.Marshal(%q) returned unexpected error: %v", want, err)
			}

			var got task
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("json.Unmarshal(%s) returned unexpected error: %v", encoded, err)
			}

			if got.Driver != want {
				t.Errorf("round trip produced %q, want %q", got.Driver, want)
			}
		})
	}
}

// An unknown driver in a JSON payload must be refused at the boundary rather
// than entering the model and failing somewhere less informative.
func TestDriverName_JSONRejectsUnknown(t *testing.T) {
	type task struct {
		Driver DriverName `json:"driver"`
	}

	var got task
	err := json.Unmarshal([]byte(`{"driver":"oci-job"}`), &got)

	if err == nil {
		t.Fatalf("json.Unmarshal accepted an unknown driver, producing %q", got.Driver)
	}

	if !errors.Is(err, ErrUnknownDriver) {
		t.Errorf("json.Unmarshal error does not wrap ErrUnknownDriver: %v", err)
	}
}

func TestDriverName_String(t *testing.T) {
	if got := DriverContainer.String(); got != "container" {
		t.Errorf("DriverContainer.String() = %q, want %q", got, "container")
	}
}
