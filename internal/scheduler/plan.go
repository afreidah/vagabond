// -------------------------------------------------------------------------------
// Task Plans
//
// Author: Alex Freidah
//
// Admission and ranking for one task, together: what job plan prints and the
// API returns. Kept here so both render the same decision.
// -------------------------------------------------------------------------------

package scheduler

// Plan is where one task would run and why not elsewhere.
type Plan struct {
	Ranking    Ranking
	Rejections []Rejection
	Retryable  bool
}

// PlanTask admits req against inputs and ranks what was admitted.
func PlanTask(req *Request, inputs []Input) Plan {
	result := Admit(req, inputs)

	return Plan{
		Ranking:    Rank(req, result.Candidates),
		Rejections: result.Rejections,
		Retryable:  result.Retryable(),
	}
}
