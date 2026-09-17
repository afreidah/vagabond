// -------------------------------------------------------------------------------
// Routing Strategy - how the scheduler orders admitted candidates
//
// Author: Alex Freidah
//
// A closed vocabulary, so a job naming a strategy that does not exist fails
// while it is being read rather than silently falling back to a different
// ordering than its author asked for.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Strategy is how the scheduler orders admitted candidates.
type Strategy string

// The routing strategies Vagabond implements.
//
// StrategyFreeFirst prefers candidates with free-tier capacity remaining, and is
// the only strategy in phase 1.
const (
	StrategyFreeFirst Strategy = "free-first"
)

var strategyNames = []Strategy{StrategyFreeFirst}

// ErrUnknownStrategy is the sentinel behind every unknown-strategy failure.
var ErrUnknownStrategy = errors.New("unknown routing strategy")

// Strategies returns the valid routing strategies in declaration order.
//
// The returned slice is a copy, so a caller rendering it into an error cannot
// reorder the vocabulary for everyone else.
func Strategies() []Strategy {
	return slices.Clone(strategyNames)
}

// Valid reports whether s is a strategy Vagabond implements.
func (s Strategy) Valid() bool {
	return slices.Contains(strategyNames, s)
}

// String returns the strategy as written in a job file.
func (s Strategy) String() string {
	return string(s)
}

// ParseStrategy converts in into a Strategy, or reports that it is not one.
func ParseStrategy(in string) (Strategy, error) {
	s := Strategy(in)
	if !s.Valid() {
		return "", unknownStrategy(in)
	}

	return s, nil
}

// MarshalText implements encoding.TextMarshaler.
func (s Strategy) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, unknownStrategy(string(s))
	}

	return []byte(s), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Strategy) UnmarshalText(text []byte) error {
	parsed, err := ParseStrategy(string(text))
	if err != nil {
		return err
	}

	*s = parsed

	return nil
}

func unknownStrategy(name string) error {
	valid := make([]string, 0, len(strategyNames))
	for _, s := range strategyNames {
		valid = append(valid, string(s))
	}

	return fmt.Errorf("%w %q: valid strategies are %s",
		ErrUnknownStrategy, name, strings.Join(valid, ", "))
}
