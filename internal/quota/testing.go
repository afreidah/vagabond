// -------------------------------------------------------------------------------
// Quota Fixtures
//
// Author: Alex Freidah
//
// Compiled budgets shaped after the free tiers behind the three fake
// providers, so the ledger can be exercised with no cloud account. Fixtures,
// not defaults: nothing ships an allowance, because the number an operator
// writes encodes their spend tolerance rather than a published fact.
//
// They live in the package rather than a test file because downstream packages
// reuse them, the same way plugin.FixtureContainer does.
// -------------------------------------------------------------------------------

package quota

// FixtureContainer is a container provider's budgets, shaped after IBM Code
// Engine: 100,000 inbound requests, 100,000 vCPU-seconds and 200,000 GB-seconds
// a month.
//
// The vCPU budget is the one that runs out first at ordinary CPU-to-memory
// ratios, which is why a container provider needs both.
func FixtureContainer() Limits {
	return mustLimits([]PoolSpec{
		{Name: "requests", Meter: MeterExecutions, Limit: 100_000, Period: PeriodMonthly},
		{Name: "cpu", Meter: MeterCPUSeconds, Limit: 100_000, Period: PeriodMonthly},
		{Name: "compute", Meter: MeterGBSeconds, Limit: 200_000, Period: PeriodMonthly},
	})
}

// FixtureFunction is a function provider's budgets, shaped after AWS Lambda:
// a million requests and 400,000 GB-seconds a month, exhausting independently.
func FixtureFunction() Limits {
	return mustLimits([]PoolSpec{
		{Name: "requests", Meter: MeterExecutions, Limit: 1_000_000, Period: PeriodMonthly},
		{Name: "compute", Meter: MeterGBSeconds, Limit: 400_000, Period: PeriodMonthly},
	})
}

// FixtureWorker is an edge provider's budgets, shaped after Cloudflare
// Workers, which is the daily reset rather than a monthly one.
func FixtureWorker() Limits {
	return mustLimits([]PoolSpec{
		{Name: "requests", Meter: MeterExecutions, Limit: 100_000, Period: PeriodDaily},
	})
}

// mustLimits compiles fixture specs, panicking on a spec this package wrote
// itself and got wrong.
func mustLimits(specs []PoolSpec) Limits {
	limits, err := NewLimits(specs)
	if err != nil {
		panic(err)
	}

	return limits
}
