// -------------------------------------------------------------------------------
// Routing - the policy admission and the scheduler apply
//
// Author: Alex Freidah
//
// Routing expresses limits, not a failover chain. The provider list is an
// allowlist and its order carries no meaning: admission removes any provider
// that cannot satisfy the task, and ranking orders the survivors by tier and
// score. A job that names three providers is not promised any of them.
// -------------------------------------------------------------------------------

package job

// -------------------------------------------------------------------------
// ROUTING
// -------------------------------------------------------------------------

// Routing is a job's provider selection policy. MaxCost makes the no-spend
// limit explicit: a job declaring zero is never dispatched to paid capacity.
type Routing struct {
	Strategy    *Strategy    `hcl:"strategy,optional"`
	Providers   []string     `hcl:"providers,optional"`
	MaxCost     *Cost        `hcl:"max_cost_usd,optional"`
	Tiers       *TierMode    `hcl:"tiers,optional"` // overrides the server's scheduling.tiers
	Constraints []Constraint `hcl:"constraint,block"`
	Affinities  []Affinity   `hcl:"affinity,block"`
}

// -------------------------------------------------------------------------
// MATCHING
// -------------------------------------------------------------------------

// Constraint is a hard requirement a provider must meet to be admitted.
//
// Attribute uses the dotted form the capability model publishes, such as
// provider.architecture. A provider that does not publish it is rejected with
// its own reason, so a typo does not read like a capacity problem. Value is
// empty for the presence operators.
type Constraint struct {
	Attribute string   `hcl:"attribute"`
	Operator  Operator `hcl:"operator"`
	Value     string   `hcl:"value,optional"`
}

// Affinity is a soft preference that raises a provider's score without
// affecting whether it is admitted. Weight is added when the comparison holds.
type Affinity struct {
	Attribute string   `hcl:"attribute"`
	Operator  Operator `hcl:"operator"`
	Value     string   `hcl:"value,optional"`
	Weight    *int     `hcl:"weight,optional"`
}
