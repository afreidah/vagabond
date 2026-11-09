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

// The architectures Vagabond schedules against. The set is closed, so widening
// it is a change to the workload model rather than to provider configuration.
const (
	ArchAMD64 Arch = "amd64"
	ArchARM64 Arch = "arm64"
)

var archNames = []Arch{ArchAMD64, ArchARM64}

// ErrUnknownArch is the sentinel behind every unknown-architecture failure.
var ErrUnknownArch = errors.New("unknown architecture")

// Arches returns the valid architectures in declaration order, as a copy.
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

// MarshalText implements encoding.TextMarshaler and validates, so an unset
// value fails here rather than in whatever reads the stored text back.
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

// unknownArch builds the ErrUnknownArch failure for name, listing valid ones.
func unknownArch(name string) error {
	valid := make([]string, 0, len(archNames))
	for _, a := range archNames {
		valid = append(valid, string(a))
	}

	return fmt.Errorf("%w %q: valid architectures are %s",
		ErrUnknownArch, name, strings.Join(valid, ", "))
}
