// -------------------------------------------------------------------------------
// Admission
//
// Author: Alex Freidah
//
// Runs every checker against every provider and assembles the verdict. This is
// the assembly point, and the order below is the load-bearing decision in the
// package: the plan table gives each provider one line, so whichever checker
// rejects first is the answer a person reads.
//
// Nothing here calls a provider. Everything admission needs was fingerprinted
// earlier and arrives as a value, which is what makes this a pure function and
// what lets a plan be computed without an account anywhere. Nomad holds the
// same line: its feasibility checks read node attributes the client reported,
// and the scheduler never calls a task driver.
// -------------------------------------------------------------------------------

package scheduler

// -------------------------------------------------------------------------
// ORDER
// -------------------------------------------------------------------------

// checkers are run in this order, and the order is the design.
//
// Policy first. A provider the job excluded is excluded whether or not it could
// have run the work, and reporting an unsupported driver instead invites a fix
// to a job that was never trying to go there.
//
// Mismatches next, cheapest first: set membership, then booleans, then integer
// comparisons, then the constraint evaluation that builds an attribute map.
// These say the pairing is impossible, which is the most actionable thing a job
// author can be told.
//
// Conditions last, because they are the least explanatory. Quota is final,
// mirroring Nomad's note that its quota iterator must come after everything
// else so that usage never counts capacity already ruled out.
func checkers() []Checker {
	return []Checker{
		allowlistChecker{},
		costChecker{},

		driverChecker{},
		archChecker{},
		imageChecker{},
		networkChecker{},
		resourcesChecker{},
		durationChecker{},
		attributeChecker{},
		constraintChecker{},

		enabledChecker{},
		healthyChecker{},
		quotaChecker{},
	}
}

// Checkers returns the admission rules in the order they run.
//
// Exported so that a plan can explain itself and a test can assert the order
// without reaching into the package. The slice is freshly built, so a caller
// reordering it changes nothing for anyone else.
func Checkers() []Checker {
	return checkers()
}

// -------------------------------------------------------------------------
// ADMISSION
// -------------------------------------------------------------------------

// Admit decides which providers can run the request.
//
// Every checker runs against every provider rather than stopping at the first
// rejection. The extra work is a handful of comparisons over a cached snapshot,
// and it buys the whole picture: a task that fails five checks against a
// provider would otherwise take five edit-and-rerun cycles to discover that.
// The first rejection is what gets rendered; the rest ride along in Also.
//
// Ordered by provider name before returning, so that plan output for the same
// inputs is byte-identical and can be diffed in CI.
func Admit(req *Request, inputs []Input) Result {
	var result Result

	rules := checkers()

	for i := range inputs {
		in := &inputs[i]

		if rejection := admitOne(req, in, rules); rejection != nil {
			result.Rejections = append(result.Rejections, *rejection)

			continue
		}

		// Cloned rather than shared. Admission runs against snapshots the
		// registry holds and reuses, so a caller that sorted a candidate's
		// drivers would change what every later plan sees.
		admitted := *in
		admitted.Capabilities = in.Capabilities.Clone()

		result.Candidates = append(result.Candidates, Candidate{
			Input:         admitted,
			EstimatedCost: in.Capabilities.EstimatedCost,
		})
	}

	result.Sort()

	return result
}

// admitOne runs every checker against one provider, or returns nil if it can
// run the request.
//
// The first rejection carries the detail because it is the one a person reads.
// Later reasons are collected as codes alone: rendering a detail nothing
// displays would cost a dozen format calls per provider on every plan.
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
// CONSTRUCTION
// -------------------------------------------------------------------------

// reject builds the rejection a checker returns.
//
// Provider is left empty and filled in by admitOne, because a checker is handed
// the input it is judging and should not have to copy a field out of it
// correctly in a dozen places.
func reject(reason Reason, detail string) *Rejection {
	return &Rejection{Reason: reason, Detail: detail}
}
