// -------------------------------------------------------------------------------
// Ranking Tests
//
// Author: Alex Freidah
//
// A score is a number nobody can check by eye, so what is asserted here is the
// orderings it has to produce and the arithmetic behind them. The last test is
// the one that matters most: the affinity the documented example actually
// writes has to change which provider wins, or the example is decoration.
// -------------------------------------------------------------------------------

package scheduler

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
)

// candidate is a provider admitted with a stated free-tier standing.
func candidate(name string, freePercent int) Candidate {
	in := fixtureInput(name, plugin.FixtureContainer)
	setFreePercent(&in, freePercent)

	return Candidate{Input: in}
}

// rankOf returns one provider's score, failing if it was not ranked.
func rankOf(t *testing.T, ranking Ranking, provider string) *ScoredCandidate {
	t.Helper()

	for i := range ranking {
		if ranking[i].Provider == provider {
			return &ranking[i]
		}
	}

	t.Fatalf("%s was not ranked", provider)

	return nil
}

// -------------------------------------------------------------------------
// HEADROOM
// -------------------------------------------------------------------------

// Spending the scarcest allowance first strands the workloads with the fewest
// eligible providers, so free-first drains the emptiest last.
func TestRankPrefersHeadroom(t *testing.T) {
	t.Parallel()

	ranking := Rank(baseRequest(), []Candidate{
		candidate("nearly-spent", 10),
		candidate("half-used", 50),
		candidate("untouched", 95),
	})

	want := []string{"untouched", "half-used", "nearly-spent"}
	if diff := cmp.Diff(want, ranking.Providers()); diff != "" {
		t.Errorf("ranking mismatch (-want +got):\n%s", diff)
	}

	selectedValue, ok := ranking.Selected()
	if !ok {
		t.Fatal("nothing was selected from a non-empty ranking")
	}

	if selectedValue.Provider != "untouched" {
		t.Errorf("selected %q, want untouched", selectedValue.Provider)
	}

	// With no affinities the score is the headroom alone, unaveraged.
	if got := selectedValue.Percent(); got != 95 {
		t.Errorf("score = %d, want 95", got)
	}
}

// A job that stated no strategy gets the one this project exists for.
func TestRankDefaultsToFreeFirst(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	if got := req.Strategy(); got != job.StrategyFreeFirst {
		t.Errorf("Strategy() = %s, want %s", got, job.StrategyFreeFirst)
	}

	stated := baseRequest()
	stated.Routing = &job.Routing{Strategy: ptr.Of(job.StrategyFreeFirst)}

	if diff := cmp.Diff(
		Rank(req, []Candidate{candidate("a", 20), candidate("b", 80)}).Providers(),
		Rank(stated, []Candidate{candidate("a", 20), candidate("b", 80)}).Providers(),
	); diff != "" {
		t.Errorf("stating the default changed the ranking:\n%s", diff)
	}
}

// A strategy jobspec validation would have rejected still has to rank rather
// than panic, because ranking is reachable from an API that did not parse a
// file and cannot assume anything was checked.
func TestRankUnknownStrategyFallsBack(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	req.Routing = &job.Routing{Strategy: ptr.Of(job.Strategy("cheapest-tuesday"))}

	ranking := Rank(req, []Candidate{candidate("a", 20), candidate("b", 80)})

	if diff := cmp.Diff([]string{"b", "a"}, ranking.Providers()); diff != "" {
		t.Errorf("ranking mismatch (-want +got):\n%s", diff)
	}
}

// A ledger that reported nonsense must not drag the mean outside the scale a
// plan prints as a percentage.
func TestRankClampsHeadroom(t *testing.T) {
	t.Parallel()

	ranking := Rank(baseRequest(), []Candidate{
		candidate("impossible", 140),
		candidate("negative", -20),
	})

	if got := rankOf(t, ranking, "impossible").Percent(); got != 100 {
		t.Errorf("score = %d, want 100", got)
	}

	if got := rankOf(t, ranking, "negative").Percent(); got != 0 {
		t.Errorf("score = %d, want 0", got)
	}
}

// -------------------------------------------------------------------------
// AFFINITIES
// -------------------------------------------------------------------------

// A satisfied affinity raises a candidate above an otherwise identical one that
// does not satisfy it.
func TestRankAffinityRaisesAMatch(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	req.Routing = &job.Routing{Affinities: []job.Affinity{{
		Attribute: plugin.MetaPrefix + "tier",
		Operator:  job.OperatorEqual,
		Value:     "paid",
		Weight:    ptr.Of(75),
	}}}

	matching := candidate("matching", 50)
	matching.Tags = map[string]string{"tier": "paid"}

	plain := candidate("plain", 50)

	ranking := Rank(req, []Candidate{matching, plain})

	if diff := cmp.Diff([]string{"matching", "plain"}, ranking.Providers()); diff != "" {
		t.Errorf("ranking mismatch (-want +got):\n%s", diff)
	}

	// Half headroom with the affinity satisfied averages to 0.75, and with it
	// unsatisfied to 0.25. An affinity nobody satisfies is not free.
	if got := rankOf(t, ranking, "matching").Percent(); got != 75 {
		t.Errorf("matching score = %d, want 75", got)
	}

	if got := rankOf(t, ranking, "plain").Percent(); got != 25 {
		t.Errorf("plain score = %d, want 25", got)
	}
}

// Weight states relative importance among a job's affinities rather than an
// absolute number of points, so satisfying the heavier of two scores higher
// than satisfying the lighter.
func TestRankAffinityWeightsAreRelative(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	req.Routing = &job.Routing{Affinities: []job.Affinity{
		{
			Attribute: plugin.MetaPrefix + "tier",
			Operator:  job.OperatorEqual,
			Value:     "paid",
			Weight:    ptr.Of(75),
		},
		{
			Attribute: plugin.MetaPrefix + "region",
			Operator:  job.OperatorEqual,
			Value:     "us-south",
			Weight:    ptr.Of(25),
		},
	}}

	heavy := candidate("heavy", 0)
	heavy.Tags = map[string]string{"tier": "paid"}

	light := candidate("light", 0)
	light.Tags = map[string]string{"region": "us-south"}

	both := candidate("both", 0)
	both.Tags = map[string]string{"tier": "paid", "region": "us-south"}

	ranking := Rank(req, []Candidate{heavy, light, both})

	want := []string{"both", "heavy", "light"}
	if diff := cmp.Diff(want, ranking.Providers()); diff != "" {
		t.Errorf("ranking mismatch (-want +got):\n%s", diff)
	}

	// Zero headroom averaged with the matched fraction of total weight: all of
	// it, three quarters of it, and a quarter of it.
	for provider, want := range map[string]int{"both": 50, "heavy": 38, "light": 13} {
		if got := rankOf(t, ranking, provider).Percent(); got != want {
			t.Errorf("%s score = %d, want %d", provider, got, want)
		}
	}
}

// An affinity nobody wrote is not one every provider failed, so it contributes
// no scorer at all rather than a zero that would halve every score identically.
func TestRankWithoutAffinitiesScoresHeadroomAlone(t *testing.T) {
	t.Parallel()

	ranking := Rank(baseRequest(), []Candidate{candidate("only", 80)})

	scored := &ranking[0]
	if got := scored.Percent(); got != 80 {
		t.Errorf("score = %d, want 80", got)
	}

	want := []Score{{Name: ScorerHeadroom, Value: 0.8}}
	if diff := cmp.Diff(want, scored.Scores); diff != "" {
		t.Errorf("scorers mismatch (-want +got):\n%s", diff)
	}
}

// The breakdown is carried so a plan can defend the number rather than ask to
// be trusted on it.
func TestRankCarriesItsReasoning(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	req.Routing = &job.Routing{Affinities: []job.Affinity{{
		Attribute: plugin.AttrFreeQuotaPercent,
		Operator:  job.OperatorGreater,
		Value:     "50",
	}}}

	ibmRanking := Rank(req, []Candidate{candidate("ibm", 80)})
	scored := &ibmRanking[0]

	want := []Score{
		{Name: ScorerHeadroom, Value: 0.8},
		{Name: ScorerAffinity, Value: 1},
	}

	if diff := cmp.Diff(want, scored.Scores); diff != "" {
		t.Errorf("scorers mismatch (-want +got):\n%s", diff)
	}
}

// -------------------------------------------------------------------------
// THE DOCUMENTED EXAMPLE
// -------------------------------------------------------------------------

// The example job prefers providers above fifty percent remaining, at weight
// 75. That affinity has to change which provider wins, or the example is
// teaching something the scheduler does not do.
func TestRankExampleAffinityChangesTheOutcome(t *testing.T) {
	t.Parallel()

	candidates := []Candidate{
		candidate("above-the-line", 55),
		candidate("below-the-line", 45),
	}

	// Both are scored on headroom alone without the affinity, and the ordering
	// is the same either way here. What the affinity changes is the distance.
	plain := Rank(baseRequest(), candidates)

	req := baseRequest()
	req.Routing = &job.Routing{Affinities: []job.Affinity{{
		Attribute: plugin.AttrFreeQuotaPercent,
		Operator:  job.OperatorGreater,
		Value:     "50",
		Weight:    ptr.Of(75),
	}}}

	withAffinity := Rank(req, candidates)

	plainGap := rankOf(t, plain, "above-the-line").Score -
		rankOf(t, plain, "below-the-line").Score

	affinityGap := rankOf(t, withAffinity, "above-the-line").Score -
		rankOf(t, withAffinity, "below-the-line").Score

	if affinityGap <= plainGap {
		t.Errorf("the affinity did not separate the candidates: %.3f then %.3f",
			plainGap, affinityGap)
	}

	// Ten points of headroom apart becomes a clear preference: one satisfies the
	// stated threshold and the other does not.
	if got := rankOf(t, withAffinity, "above-the-line").Percent(); got != 78 {
		t.Errorf("above-the-line score = %d, want 78", got)
	}

	if got := rankOf(t, withAffinity, "below-the-line").Percent(); got != 23 {
		t.Errorf("below-the-line score = %d, want 23", got)
	}
}

// An affinity strong enough to overturn the headroom ordering does so, which is
// the whole reason a job is allowed to state one.
func TestRankAffinityCanOverturnHeadroom(t *testing.T) {
	t.Parallel()

	req := baseRequest()
	req.Routing = &job.Routing{Affinities: []job.Affinity{{
		Attribute: plugin.MetaPrefix + "region",
		Operator:  job.OperatorEqual,
		Value:     "us-south",
	}}}

	fuller := candidate("fuller", 90)

	preferred := candidate("preferred", 60)
	preferred.Tags = map[string]string{"region": "us-south"}

	ranking := Rank(req, []Candidate{fuller, preferred})

	if diff := cmp.Diff([]string{"preferred", "fuller"}, ranking.Providers()); diff != "" {
		t.Errorf("ranking mismatch (-want +got):\n%s", diff)
	}
}

// -------------------------------------------------------------------------
// DETERMINISM
// -------------------------------------------------------------------------

// Equal scores keep the order admission produced, which is by provider name.
func TestRankBreaksTiesByProvider(t *testing.T) {
	t.Parallel()

	// Admission hands ranking a name-ordered slice, so a stable sort is all a
	// tiebreak needs to be.
	candidates := []Candidate{
		candidate("alpha", 50),
		candidate("bravo", 50),
		candidate("zulu", 50),
	}

	want := []string{"alpha", "bravo", "zulu"}
	if diff := cmp.Diff(want, Rank(baseRequest(), candidates).Providers()); diff != "" {
		t.Errorf("ranking mismatch (-want +got):\n%s", diff)
	}
}

// Plan output is diffable in CI only if the same inputs produce the same order
// every run.
func TestRankIsRepeatable(t *testing.T) {
	t.Parallel()

	candidates := []Candidate{
		candidate("alpha", 50),
		candidate("bravo", 50),
		candidate("charlie", 80),
		candidate("delta", 50),
	}

	first := Rank(baseRequest(), candidates).Providers()

	for range 5 {
		if diff := cmp.Diff(first, Rank(baseRequest(), candidates).Providers()); diff != "" {
			t.Fatalf("ranking changed between runs:\n%s", diff)
		}
	}
}

// Ranking must not change the candidates it was handed, so that a caller can
// rank the same admission result under two policies and compare them.
func TestRankDoesNotMutateCandidates(t *testing.T) {
	t.Parallel()

	candidates := []Candidate{candidate("zulu", 10), candidate("alpha", 90)}

	Rank(baseRequest(), candidates)

	want := []string{"zulu", "alpha"}

	got := make([]string, 0, len(candidates))
	for i := range candidates {
		got = append(got, candidates[i].Provider)
	}

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ranking reordered its input (-want +got):\n%s", diff)
	}
}

// -------------------------------------------------------------------------
// NOTHING ADMITTED
// -------------------------------------------------------------------------

// Nowhere to run is a different outcome from somewhere unappealing, and a
// caller has to be able to tell them apart.
func TestRankNothingAdmitted(t *testing.T) {
	t.Parallel()

	ranking := Rank(baseRequest(), nil)

	if len(ranking) != 0 {
		t.Errorf("ranked %d candidates from none", len(ranking))
	}

	if _, ok := ranking.Selected(); ok {
		t.Error("an empty ranking selected something")
	}
}
