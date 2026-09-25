// -------------------------------------------------------------------------------
// Budgets
//
// Author: Alex Freidah
//
// Every pool a deployment enforces, in two layers: each provider's total, and
// each namespace's optional share of a provider. An execution charges both and
// needs room in both, so the shares can never add up past the total.
// -------------------------------------------------------------------------------

package quota

// Budgets is every compiled pool, by provider and by namespace.
type Budgets struct {
	Totals     map[string]Limits            // by provider
	Namespaces map[string]map[string]Limits // by namespace, then provider
}

// Total returns a provider's own pools. The zero value enforces nothing.
func (b Budgets) Total(provider string) Limits {
	return b.Totals[provider]
}

// Share returns a namespace's share of a provider. The zero value, for a
// namespace that declared none, leaves the provider's total as the only limit.
func (b Budgets) Share(namespace, provider string) Limits {
	return b.Namespaces[namespace][provider]
}
