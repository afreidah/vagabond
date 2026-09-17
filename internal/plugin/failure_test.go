// -------------------------------------------------------------------------------
// Failure Taxonomy Tests
//
// Author: Alex Freidah
//
// The reroute decision is the reason this taxonomy exists, so it is asserted as
// a property of the class rather than only through examples. The HTTP mapping
// gets status-by-status coverage because three plugins written months apart
// will otherwise drift.
// -------------------------------------------------------------------------------

package plugin

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

var errUnderlying = errors.New("connection reset")

// -------------------------------------------------------------------------
// CLASSES
// -------------------------------------------------------------------------

func TestClass_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input Class
		want  bool
	}{
		{name: "infrastructure", input: ClassInfrastructure, want: true},
		{name: "internal", input: ClassInternal, want: true},
		{name: "empty", input: "", want: false},
		{name: "workload is not an error class", input: "workload", want: false},
		{name: "case variant", input: "Internal", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("Class(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// Only infrastructure may be rerouted. An internal failure reproduces wherever
// it is sent, so rerouting one spends free-tier capacity to reach the same
// answer again.
func TestClass_Reroutable(t *testing.T) {
	if !ClassInfrastructure.Reroutable() {
		t.Error("infrastructure failures are not reroutable")
	}

	if ClassInternal.Reroutable() {
		t.Error("internal failures are reroutable")
	}
}

// Exactly one class may be rerouted. If a third ever qualifies, that is a
// decision to make deliberately rather than inherit.
func TestClass_OnlyOneClassIsReroutable(t *testing.T) {
	count := 0

	for _, c := range Classes() {
		if c.Reroutable() {
			count++
		}
	}

	if count != 1 {
		t.Errorf("%d classes are reroutable, want exactly 1", count)
	}
}

func TestClass_String(t *testing.T) {
	if got := ClassInfrastructure.String(); got != "infrastructure" {
		t.Errorf("ClassInfrastructure.String() = %q, want %q", got, "infrastructure")
	}

	if got := ClassInternal.String(); got != "internal" {
		t.Errorf("ClassInternal.String() = %q, want %q", got, "internal")
	}
}

func TestClasses_ReturnsCopy(t *testing.T) {
	first := Classes()
	first[0] = "mutated"

	if Classes()[0] == "mutated" {
		t.Error("Classes() exposed the package-level vocabulary to mutation")
	}
}

// -------------------------------------------------------------------------
// ERROR BEHAVIOUR
// -------------------------------------------------------------------------

// A caller that only needs to know a provider operation failed should not have
// to know this type exists.
func TestError_IsProvider(t *testing.T) {
	err := Infrastructure(errUnderlying)

	if !errors.Is(err, ErrProvider) {
		t.Error("errors.Is(err, ErrProvider) = false")
	}
}

// Unwrapping must reach whatever the provider SDK returned, or a caller cannot
// inspect the real cause.
func TestError_UnwrapsToCause(t *testing.T) {
	err := Infrastructure(errUnderlying)

	if !errors.Is(err, errUnderlying) {
		t.Error("the underlying cause is not reachable through errors.Is")
	}
}

func TestError_AsRecoversClassification(t *testing.T) {
	var err error = Internal(errUnderlying)

	var provider *Error
	if !errors.As(err, &provider) {
		t.Fatalf("errors.As did not yield *Error, got %T", err)
	}

	if provider.Class != ClassInternal {
		t.Errorf("Class = %q, want %q", provider.Class, ClassInternal)
	}
}

func TestError_MessageNamesProviderAndOp(t *testing.T) {
	err := Infrastructure(errUnderlying).WithContext("ibm-code-engine", "submit")
	msg := err.Error()

	for _, want := range []string{"infrastructure", "ibm-code-engine", "submit", "connection reset"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
}

// Context is filled in by the dispatcher on the way out, so an error built by a
// plugin without it is still well formed.
func TestError_MessageWithoutContext(t *testing.T) {
	err := Internal(errUnderlying)

	if msg := err.Error(); !strings.Contains(msg, "internal") {
		t.Errorf("message %q does not name the class", msg)
	}
}

// -------------------------------------------------------------------------
// CONSTRUCTORS
// -------------------------------------------------------------------------

// An internal failure must never be retried: the same request produces the same
// failure wherever it goes.
func TestInternal_IsNeverRetryable(t *testing.T) {
	err := Internal(errUnderlying)

	if err.Retryable {
		t.Error("an internal failure is marked retryable")
	}

	if err.Reroutable() {
		t.Error("an internal failure is marked reroutable")
	}
}

func TestInfrastructure_IsRetryable(t *testing.T) {
	err := Infrastructure(errUnderlying)

	if !err.Retryable {
		t.Error("an infrastructure failure is not marked retryable")
	}

	if !err.Reroutable() {
		t.Error("an infrastructure failure is not reroutable")
	}
}

// -------------------------------------------------------------------------
// HTTP CLASSIFICATION
// -------------------------------------------------------------------------

func TestClassifyHTTP(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		wantClass     Class
		wantRetryable bool
	}{
		{name: "bad request is our fault", status: http.StatusBadRequest, wantClass: ClassInternal},
		{name: "unauthorized is our fault", status: http.StatusUnauthorized, wantClass: ClassInternal},
		{name: "not found is our fault", status: http.StatusNotFound, wantClass: ClassInternal},
		{name: "unprocessable is our fault", status: http.StatusUnprocessableEntity, wantClass: ClassInternal},

		{
			name: "request timeout is the provider declining", status: http.StatusRequestTimeout,
			wantClass: ClassInfrastructure, wantRetryable: true,
		},
		{
			name: "rate limited is the provider declining", status: http.StatusTooManyRequests,
			wantClass: ClassInfrastructure, wantRetryable: true,
		},
		{
			name: "internal server error is theirs", status: http.StatusInternalServerError,
			wantClass: ClassInfrastructure, wantRetryable: true,
		},
		{
			name: "service unavailable is theirs", status: http.StatusServiceUnavailable,
			wantClass: ClassInfrastructure, wantRetryable: true,
		},
		{
			name: "gateway timeout is theirs", status: http.StatusGatewayTimeout,
			wantClass: ClassInfrastructure, wantRetryable: true,
		},
		{
			name: "an unrecognized status is not evidence of our bug", status: 0,
			wantClass: ClassInfrastructure, wantRetryable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ClassifyHTTP(tt.status, 0, errUnderlying)

			if err.Class != tt.wantClass {
				t.Errorf("Class = %q, want %q", err.Class, tt.wantClass)
			}

			if err.Retryable != tt.wantRetryable {
				t.Errorf("Retryable = %v, want %v", err.Retryable, tt.wantRetryable)
			}
		})
	}
}

// A 400 is infrastructure-shaped but must never be retried, which is why
// Retryable is independent of Class rather than derived from it.
func TestClassifyHTTP_BadRequestIsNotRetryable(t *testing.T) {
	err := ClassifyHTTP(http.StatusBadRequest, 0, errUnderlying)

	if err.Retryable {
		t.Error("a 400 is marked retryable")
	}
}

// The Retry-After hint has to survive, or every plugin reimplements backoff.
func TestClassifyHTTP_CarriesRetryAfter(t *testing.T) {
	err := ClassifyHTTP(http.StatusTooManyRequests, 30*time.Second, errUnderlying)

	if err.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", err.RetryAfter)
	}
}

func TestClassifyHTTP_PreservesStatusAndCause(t *testing.T) {
	err := ClassifyHTTP(http.StatusServiceUnavailable, 0, errUnderlying)

	if !strings.Contains(err.Error(), "503") {
		t.Errorf("message %q does not carry the status", err)
	}

	if !errors.Is(err, errUnderlying) {
		t.Error("the underlying cause was lost")
	}
}
