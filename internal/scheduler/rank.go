// -------------------------------------------------------------------------------
// Ranking
//
// Author: Alex Freidah
//
// Admission says which providers can run the task. This says which one should,
// and why, which is the harder half to explain: a rejection names the rule it
// broke, while a score is a number that has to be defensible on its own.
//
// So a candidate carries the parts it was built from, not only the total. A
// plan that can say "eighty percent headroom, no affinity satisfied" is
// answering a question; one that says "score 40" is asking the reader to trust
// it. Nomad keeps the same breakdown on RankedNode.Scores for the same reason.
//
// Nothing here calls a provider. Ranking is a pure function of what admission
// produced, and every value it reads was fingerprinted before the plan began.
// -------------------------------------------------------------------------------

package scheduler

import (
	"math"
	"sort"

	"github.com/afreidah/vagabond/internal/job"
)

// -------------------------------------------------------------------------
// SCORERS
// -------------------------------------------------------------------------

// The scorers that can contribute to a candidate's score.
//
// Named because they appear in plan output and in whatever a CI system reads,
// which makes them permanent surface the same way rejection reasons are.
const (
	ScorerHeadroom = "headroom"
	ScorerAffinity = "affinity"
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
//
// Score is the mean of Scores rather than their sum. Nomad averages for the
// same reason: a sum makes a job with four affinities score on a different
// scale than a job with one, and neither number means anything next to the
// other.
type ScoredCandidate struct {
	Candidate
	Score  float64
	Scores []Score
}

// Percent renders the score the way a plan prints it.
//
// Scoring is done in floating point because averaging demands it, and shown as
// an integer because a table reading "score 91" is worth more to a person than
// one reading 0.9125.
func (s *ScoredCandidate) Percent() int {
	return int(math.Round(s.Score * 100))
}

// Ranking is every candidate, best first.
//
// The whole ordering rather than a winner. Rerouting after an infrastructure
// failure needs the second choice, and a ranking reduced to its head cannot
// produce one without running the whole plan again against a changed world.
type Ranking []ScoredCandidate

// Selected returns the candidate a job should be dispatched to.
//
// Reports false when nothing was admitted, which is a different outcome from a
// candidate scoring zero: one has nowhere to run and the other has somewhere
// unappealing.
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

// Rank orders admitted candidates by how well each suits the request.
//
// Stable, and the candidates admission produced are already ordered by provider
// name, so equal scores keep that order. Plan output for the same inputs is
// byte-identical across runs, which is what lets it be diffed in CI.
func Rank(req *Request, candidates []Candidate) Ranking {
	ranked := make(Ranking, 0, len(candidates))

	for i := range candidates {
		ranked = append(ranked, score(req, &candidates[i]))
	}

	sort.SliceStable(ranked, func(a, b int) bool {
		return ranked[a].Score > ranked[b].Score
	})

	return ranked
}

// score computes one candidate's standing and the parts it came from.
func score(req *Request, candidate *Candidate) ScoredCandidate {
	scores := []Score{{
		Name:  ScorerHeadroom,
		Value: baseScorer(req.Strategy())(candidate),
	}}

	// Left out entirely when the job stated no preference, rather than averaged
	// in as a zero. A preference nobody expressed is not one every provider
	// failed, and scoring it as one would halve every candidate identically to
	// say nothing. Nomad's affinity iterator skips the same way.
	if affinities := req.Affinities(); len(affinities) > 0 {
		scores = append(scores, Score{
			Name:  ScorerAffinity,
			Value: AffinityScore(candidate.Attributes(), affinities),
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

// baseScorer returns the scorer a strategy ranks by.
//
// One strategy exists, so this reads as a formality. It is here because the
// alternative is scoring inlined into Rank, and a second strategy would then
// arrive as a conditional inside the function that averages, rather than as a
// line in this switch. Affinities apply on top of whichever base is chosen.
func baseScorer(strategy job.Strategy) func(*Candidate) float64 {
	switch strategy {
	case job.StrategyFreeFirst:
		return headroomScore

	default:
		return headroomScore
	}
}

// headroomScore is what free-first prefers: the provider with the most
// free-tier allowance left.
//
// The point is not that a fuller provider runs the work better, because it does
// not. It is that spending the scarcest allowance first strands the workloads
// with the fewest eligible providers, and those are the ones with nowhere else
// to go. Draining the emptiest last keeps the most options open for whatever
// arrives next.
func headroomScore(candidate *Candidate) float64 {
	return clamp(float64(candidate.Quota.FreePercent) / 100)
}

// clamp keeps a scorer inside the range every other scorer is averaged against.
//
// A ledger that reported more than a full allowance, or a negative one, would
// otherwise let a single scorer drag the mean outside the scale the plan prints
// as a percentage.
func clamp(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}
