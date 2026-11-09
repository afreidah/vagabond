// -------------------------------------------------------------------------------
// Admission
//
// Author: Alex Freidah
//
// Runs every checker against every provider and assembles the verdict. The
// checker order is the load-bearing decision: the plan table gives each
// provider one line, so whichever checker rejects first is the answer a person
// reads. Nothing here calls a provider; everything arrives as a value.
// -------------------------------------------------------------------------------

package scheduler

import "fmt"

// -------------------------------------------------------------------------
// ORDER
// -------------------------------------------------------------------------

// checkers returns the admission rules in run order, which decides the
// rejection a plan shows: policy first; availability before mismatches, since
// a disabled or unhealthy provider's stale snapshot would otherwise fail on
// missing drivers; mismatches cheapest first; capacity last, so a provider that
// can never run the driver says so rather than report its quota.
func checkers() []Checker {
	return []Checker{
		allowlistChecker{},
		costChecker{},

		enabledChecker{},
		healthyChecker{},

		driverChecker{},
		archChecker{},
		imageChecker{},
		networkChecker{},
		resourcesChecker{},
		durationChecker{},
		attributeChecker{},
		constraintChecker{},

		quotaChecker{},
	}
}

// Checkers returns the admission rules in the order they run, freshly built so
// a caller reordering them changes nothing for anyone else.
func Checkers() []Checker {
	return checkers()
}

// -------------------------------------------------------------------------
// ADMISSION
// -------------------------------------------------------------------------

// Admit decides which providers can run the request. Every checker runs
// against every provider rather than stopping at the first rejection, so a
// plan shows every fix at once; the result is ordered by provider name.
func Admit(req *Request, inputs []Input) Result {
	var result Result

	rules := checkers()

	for i := range inputs {
		in := &inputs[i]

		members, rejection := admitProvider(req, in, rules)
		if rejection != nil {
			result.Rejections = append(result.Rejections, *rejection)

			continue
		}

		// Cloned, since the registry reuses its snapshots across plans
		admitted := *in
		admitted.Capabilities = in.Capabilities.Clone()

		result.Candidates = append(result.Candidates, Candidate{
			Input:         admitted,
			EstimatedCost: in.Capabilities.EstimatedCost,
			Members:       members,
		})
	}

	result.Sort()

	return result
}

// admitProvider admits one provider. A pool is admitted when a member passes
// every check on its own, since its properties are spread across its nodes,
// and returns the members that passed; otherwise the rejection is from the
// member that failed the fewest checks.
func admitProvider(req *Request, in *Input, rules []Checker) ([]string, *Rejection) {
	if len(in.Capabilities.Members) == 0 {
		return nil, admitOne(req, in, rules)
	}

	var (
		passed  []string
		closest *Rejection
	)

	for i := range in.Capabilities.Members {
		member := &in.Capabilities.Members[i]

		// The member's snapshot and labels, with the pool's quota, tags and health
		as := *in
		as.Capabilities = member.Capabilities
		as.Labels = member.Labels

		rejection := admitOne(req, &as, rules)
		if rejection == nil {
			passed = append(passed, member.Name)

			continue
		}

		if closest == nil || len(rejection.Also) < len(closest.Also) {
			rejection.Detail = fmt.Sprintf("closest node %s: %s", member.Name, rejection.Detail)
			closest = rejection
		}
	}

	if len(passed) > 0 {
		return passed, nil
	}

	return nil, closest
}

// admitOne runs every checker against one provider, or returns nil if it can
// run the request. Only the first rejection keeps its detail, since it is the
// one a person reads; later ones add only their reason to Also.
func admitOne(req *Request, in *Input, rules []Checker) *Rejection {
	var first *Rejection

	for _, checker := range rules {
		rejection := checker.Check(req, in)
		if rejection == nil {
			continue
		}

		if first == nil {
			rejection.Provider = in.Provider
			first = rejection

			continue
		}

		first.Also = append(first.Also, rejection.Reason)
	}

	return first
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// reject builds the rejection a checker returns, with Provider left for
// admitOne to fill in.
func reject(reason Reason, detail string) *Rejection {
	return &Rejection{Reason: reason, Detail: detail}
}
