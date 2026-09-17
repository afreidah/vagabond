// -------------------------------------------------------------------------------
// Architecture - the CPU architectures Vagabond schedules against
//
// Author: Alex Freidah
//
// A closed vocabulary following the same shape as DriverName. Validation is
// exact and rejects case variants, so one spelling reaches job files and
// persisted rows rather than two.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Arch is a CPU architecture a task requires or a provider offers.
type Arch string

// The architectures Vagabond schedules against.
//
// The set is closed and deliberately small. A provider offering something else
// cannot be matched by a job that cannot name it, so widening this is a change
// to the workload model rather than provider configuration.
const (
	ArchAMD64 Arch = "amd64"
	ArchARM64 Arch = "arm64"
)

var archNames = []Arch{ArchAMD64, ArchARM64}

// ErrUnknownArch is the sentinel behind every unknown-architecture failure.
var ErrUnknownArch = errors.New("unknown architecture")

// Arches returns the valid architectures in declaration order.
//
// The returned slice is a copy, so a caller rendering it into an error cannot
// reorder the vocabulary for everyone else.
func Arches() []Arch {
	return slices.Clone(archNames)
}

// Valid reports whether a is an architecture Vagabond schedules against.
func (a Arch) Valid() bool {
	return slices.Contains(archNames, a)
}

// String returns the architecture as written in a job file.
func (a Arch) String() string {
	return string(a)
}

// ParseArch converts s into an Arch, or reports that it is not one.
func ParseArch(s string) (Arch, error) {
	a := Arch(s)
	if !a.Valid() {
		return "", unknownArch(s)
	}

	return a, nil
}

// MarshalText implements encoding.TextMarshaler, validating for the same reason
// DriverName does: an unset value written to a column defers the failure to
// whatever reads it back, where the context that would explain it is gone.
func (a Arch) MarshalText() ([]byte, error) {
	if !a.Valid() {
		return nil, unknownArch(string(a))
	}

	return []byte(a), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (a *Arch) UnmarshalText(text []byte) error {
	parsed, err := ParseArch(string(text))
	if err != nil {
		return err
	}

	*a = parsed

	return nil
}

func unknownArch(name string) error {
	valid := make([]string, 0, len(archNames))
	for _, a := range archNames {
		valid = append(valid, string(a))
	}

	return fmt.Errorf("%w %q: valid architectures are %s",
		ErrUnknownArch, name, strings.Join(valid, ", "))
}
