// -------------------------------------------------------------------------------
// Routing - the policy admission and the scheduler apply
//
// Author: Alex Freidah
//
// Routing expresses preference and limits, not a failover chain. The provider
// list is an allowlist and an ordering hint; admission still removes any
// provider that cannot satisfy the task, and the scheduler scores whatever
// survives. A job that names three providers is not promised any of them.
// -------------------------------------------------------------------------------

package job

// -------------------------------------------------------------------------
// ROUTING
// -------------------------------------------------------------------------

// Routing is a job's provider selection policy.
//
// MaxCost exists to make the free-tier guarantee explicit rather than implied.
// A job declaring zero must never be dispatched to paid capacity, and an
// integer comparison against zero is what makes that check exact.
type Routing struct {
	Strategy    *Strategy    `hcl:"strategy,optional"`
	Providers   []string     `hcl:"providers,optional"`
	MaxCost     *Cost        `hcl:"max_cost_usd,optional"`
	Constraints []Constraint `hcl:"constraint,block"`
	Affinities  []Affinity   `hcl:"affinity,block"`
}

// -------------------------------------------------------------------------
// MATCHING
// -------------------------------------------------------------------------

// Constraint is a hard requirement a provider must meet to be admitted.
//
// Attribute names the provider attribute to test, using the dotted form the
// capability model publishes, such as provider.architecture. A constraint on an
// attribute no provider publishes rejects that provider with its own reason
// rather than matching nothing silently, so that a typo does not read like a
// capacity problem.
//
// Value is empty for the presence operators, which ask only whether the
// provider published the attribute at all.
type Constraint struct {
	Attribute string   `hcl:"attribute"`
	Operator  Operator `hcl:"operator"`
	Value     string   `hcl:"value,optional"`
}

// Affinity is a soft preference that raises a provider's score without
// affecting whether it is admitted.
//
// Weight is the contribution to the score when the comparison holds. An
// unsatisfied affinity never removes a candidate, which is the whole difference
// between this and Constraint.
type Affinity struct {
	Attribute string   `hcl:"attribute"`
	Operator  Operator `hcl:"operator"`
	Value     string   `hcl:"value,optional"`
	Weight    *int     `hcl:"weight,optional"`
}
