// -------------------------------------------------------------------------------
// Constraint Operators
//
// Author: Alex Freidah
//
// How a constraint or affinity compares a provider attribute against a value.
// The vocabulary is closed, so a misspelled operator fails while the file is
// read rather than matching nothing and reading as a capacity problem. It is
// deliberately small: placement and version operators such as regexp, semver
// and distinct_hosts answer questions a provider attribute does not raise.
// -------------------------------------------------------------------------------

package job

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// -------------------------------------------------------------------------
// OPERATORS
// -------------------------------------------------------------------------

// Operator is how a constraint compares an attribute against a value.
type Operator string

// The operators Vagabond implements.
//
// OperatorSetContains tests membership in a set-valued attribute, such as the
// architectures a provider offers, where equality would compare the whole set.
// OperatorIsSet and OperatorIsNotSet ignore the value and ask only whether the
// provider published the attribute.
const (
	OperatorEqual        Operator = "="
	OperatorNotEqual     Operator = "!="
	OperatorLess         Operator = "<"
	OperatorLessEqual    Operator = "<="
	OperatorGreater      Operator = ">"
	OperatorGreaterEqual Operator = ">="
	OperatorSetContains  Operator = "set_contains"
	OperatorIsSet        Operator = "is_set"
	OperatorIsNotSet     Operator = "is_not_set"
)

var operatorNames = []Operator{
	OperatorEqual,
	OperatorNotEqual,
	OperatorLess,
	OperatorLessEqual,
	OperatorGreater,
	OperatorGreaterEqual,
	OperatorSetContains,
	OperatorIsSet,
	OperatorIsNotSet,
}

// orderingOperators compare magnitude rather than identity, and so need both
// sides to be comparable as numbers, durations, or text.
var orderingOperators = []Operator{
	OperatorLess,
	OperatorLessEqual,
	OperatorGreater,
	OperatorGreaterEqual,
}

// presenceOperators ask only whether an attribute exists, so a job using one
// writes no value.
var presenceOperators = []Operator{OperatorIsSet, OperatorIsNotSet}

// ErrUnknownOperator is the sentinel behind every unrecognized operator.
var ErrUnknownOperator = errors.New("unknown operator")

// -------------------------------------------------------------------------
// VOCABULARY
// -------------------------------------------------------------------------

// Operators returns the valid operators in declaration order, as a copy.
func Operators() []Operator {
	return slices.Clone(operatorNames)
}

// Valid reports whether o is an operator Vagabond implements.
func (o Operator) Valid() bool {
	return slices.Contains(operatorNames, o)
}

// String returns the operator as written in a job file.
func (o Operator) String() string {
	return string(o)
}

// Ordering reports whether the operator compares magnitude.
func (o Operator) Ordering() bool {
	return slices.Contains(orderingOperators, o)
}

// Presence reports whether the operator asks only whether an attribute exists,
// and therefore needs no value.
func (o Operator) Presence() bool {
	return slices.Contains(presenceOperators, o)
}

// ParseOperator converts s into an Operator, or reports that it is not one.
func ParseOperator(s string) (Operator, error) {
	o := Operator(s)
	if !o.Valid() {
		return "", unknownOperator(s)
	}

	return o, nil
}

// MarshalText implements encoding.TextMarshaler.
func (o Operator) MarshalText() ([]byte, error) {
	if !o.Valid() {
		return nil, unknownOperator(string(o))
	}

	return []byte(o), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (o *Operator) UnmarshalText(text []byte) error {
	parsed, err := ParseOperator(string(text))
	if err != nil {
		return err
	}

	*o = parsed

	return nil
}

// unknownOperator builds the ErrUnknownOperator failure for name, listing the
// valid set.
func unknownOperator(name string) error {
	valid := make([]string, 0, len(operatorNames))
	for _, o := range operatorNames {
		valid = append(valid, string(o))
	}

	return fmt.Errorf("%w %q: valid operators are %s",
		ErrUnknownOperator, name, strings.Join(valid, ", "))
}
