// -------------------------------------------------------------------------------
// Constraint Operators
//
// Author: Alex Freidah
//
// How a constraint or affinity compares a provider attribute against a value.
// A closed vocabulary, like every other thing a job file spells out, so that a
// misspelled operator fails while the file is being read rather than matching
// nothing and reading as a capacity problem.
//
// Deliberately smaller than Nomad's, which also carries regexp, version,
// semver, set_contains_all, set_contains_any, distinct_hosts, and
// distinct_property. Those answer questions about node placement and software
// versions that a provider attribute does not raise. Each one added is surface
// that has to keep working, so they arrive when a job needs them.
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
// OperatorSetContains is the one that needs explaining. Some attributes hold a
// set rather than a single value, because a provider may offer several
// architectures or satisfy several drivers, and equality against a set asks
// whether the whole set is that one value. Membership is almost always what an
// author means, and it has its own spelling so that the other reading stays
// available and neither is a guess.
//
// OperatorIsSet and OperatorIsNotSet ignore the value entirely, asking only
// whether the provider published the attribute at all.
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

// Operators returns the valid operators in declaration order.
//
// The returned slice is a copy, so a caller rendering it into an error cannot
// reorder the vocabulary for everyone else.
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

func unknownOperator(name string) error {
	valid := make([]string, 0, len(operatorNames))
	for _, o := range operatorNames {
		valid = append(valid, string(o))
	}

	return fmt.Errorf("%w %q: valid operators are %s",
		ErrUnknownOperator, name, strings.Join(valid, ", "))
}
