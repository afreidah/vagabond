// -------------------------------------------------------------------------------
// Ranking
//
// Author: Alex Freidah
//
// Admission says which providers can run the task; this says which one should.
// A candidate carries the scores it was built from, not only the total, so a
// plan can explain a ranking rather than print a number. Ranking is a pure
// function of what admission produced.
// -------------------------------------------------------------------------------

package scheduler

import (
	"cmp"
	"math"
	"slices"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/quota"
)

// -------------------------------------------------------------------------
// SCORERS
// -------------------------------------------------------------------------

// The scorers that can contribute to a candidate's score. Their names appear
// in plan output and are stable surface, as rejection reasons are.
const (
	ScorerHeadroom = "headroom"
	ScorerAffinity = "affinity"
	ScorerTier     = "tier"
)

// Score is one scorer's verdict on a candidate, from zero to one.
type Score struct {
	Name  string
	Value float64
}

// -------------------------------------------------------------------------
// RESULTS
// -------------------------------------------------------------------------

// ScoredCandidate is a candidate and the reasoning behind where it ranked.
// Score is the mean of Scores rather than their sum, so jobs with different
// numbers of scorers score on one scale.
type ScoredCandidate struct {
	Candidate
	Score  float64
	Scores []Score
}

// Percent renders the score as the whole percentage a plan prints.
func (s *ScoredCandidate) Percent() int {
	return int(math.Round(s.Score * 100))
}

// Ranking is every candidate, best first: the whole ordering rather than a
// winner, since rerouting needs the second choice.
type Ranking []ScoredCandidate

// Selected returns the candidate a job should be dispatched to, or false when
// nothing was admitted.
func (r Ranking) Selected() (ScoredCandidate, bool) {
	if len(r) == 0 {
		return ScoredCandidate{}, false
	}

	return r[0], true
}

// Providers returns the ranked providers by name, best first.
func (r Ranking) Providers() []string {
	names := make([]string, 0, len(r))
	for i := range r {
		names = append(names, r[i].Provider)
	}

	return names
}

// -------------------------------------------------------------------------
// RANKING
// -------------------------------------------------------------------------

// Rank orders admitted candidates by how well each suits the request. Under
// strict tiers a lower tier always ranks first; under weighted tiers the tier
// is one more scorer. The sort is stable over admission's name order, so plan
// output is byte-identical across runs.
func Rank(req *Request, candidates []Candidate) Ranking {
	mode := req.Tiers()
	span := tierSpanOf(candidates)
	ranked := make(Ranking, 0, len(candidates))

	for i := range candidates {
		ranked = append(ranked, score(req, &candidates[i], mode, span))
	}

	slices.SortStableFunc(ranked, func(a, b ScoredCandidate) int {
		if mode == job.TiersStrict {
			if c := cmp.Compare(a.Tier, b.Tier); c != 0 {
				return c
			}
		}

		return cmp.Compare(b.Score, a.Score)
	})

	return ranked
}

// score computes one candidate's standing and the parts it came from.
func score(req *Request, candidate *Candidate, mode job.TierMode, span tierSpan) ScoredCandidate {
	scores := []Score{{
		Name:  ScorerHeadroom,
		Value: baseScorer(req.Strategy())(candidate, req.Execution),
	}}

	// Under strict tiers the tier is the ordering itself, so only weighted score
	if mode == job.TiersWeighted {
		scores = append(scores, Score{Name: ScorerTier, Value: span.score(candidate.Tier)})
	}

	// No stated preference leaves the scorer out rather than averaging in a zero
	if affinities := req.Affinities(); len(affinities) > 0 {
		scores = append(scores, Score{
			Name:  ScorerAffinity,
			Value: AffinityScore(candidate.Attributes(req.Execution), affinities),
		})
	}

	return ScoredCandidate{
		Candidate: *candidate,
		Score:     mean(scores),
		Scores:    scores,
	}
}

// mean averages the scorers into the number a plan prints.
func mean(scores []Score) float64 {
	if len(scores) == 0 {
		return 0
	}

	sum := 0.0
	for _, s := range scores {
		sum += s.Value
	}

	return sum / float64(len(scores))
}

// -------------------------------------------------------------------------
// STRATEGIES
// -------------------------------------------------------------------------

// baseScorer returns the scorer a strategy ranks by, with affinities applied
// on top. A second strategy is a case here rather than a branch inside Rank.
func baseScorer(strategy job.Strategy) func(*Candidate, quota.Execution) float64 {
	switch strategy {
	case job.StrategyFreeFirst:
		return headroomScore

	default:
		return headroomScore
	}
}

// headroomScore is what free-first prefers: the provider with the most
// allowance left in the pools this task charges, so scarce allowance is kept
// for workloads with nowhere else to go.
func headroomScore(candidate *Candidate, e quota.Execution) float64 {
	return clamp(float64(candidate.FreePercent(e)) / 100)
}

// -------------------------------------------------------------------------
// TIERS
// -------------------------------------------------------------------------

// tierSpan is the lowest and highest tier among the admitted candidates.
type tierSpan struct {
	low, high int
}

// tierSpanOf measures the tiers the candidates cover. Measured over what was
// admitted, so a tier that is out of the running does not compress the rest.
func tierSpanOf(candidates []Candidate) tierSpan {
	if len(candidates) == 0 {
		return tierSpan{}
	}

	span := tierSpan{low: candidates[0].Tier, high: candidates[0].Tier}
	for i := range candidates {
		span.low = min(span.low, candidates[i].Tier)
		span.high = max(span.high, candidates[i].Tier)
	}

	return span
}

// score places a tier on the span: the lowest scores one, the highest zero.
// With a single tier present every candidate scores one, since none is less
// preferred than another.
func (s tierSpan) score(tier int) float64 {
	if s.high == s.low {
		return 1
	}

	return clamp(float64(s.high-tier) / float64(s.high-s.low))
}

// clamp keeps a scorer between zero and one, so a ledger reporting more than a
// full allowance, or a negative one, cannot drag the mean off the scale.
func clamp(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}
