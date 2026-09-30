// -------------------------------------------------------------------------------
// Tier Modes - how provider tiers order the admitted candidates
//
// Author: Alex Freidah
//
// Every provider has a tier, lower preferred. The mode says how strongly: in
// strict mode tier comes before score, so a higher tier is used only when no
// lower one can run the task; in weighted mode tier is one score among the
// others. Set server-wide in the scheduling block and per job in routing.
// -------------------------------------------------------------------------------

package job

import "slices"

// TierMode is how provider tiers order the admitted candidates.
type TierMode string

// The tier modes Vagabond implements.
const (
	TiersStrict   TierMode = "strict"
	TiersWeighted TierMode = "weighted"
)

// DefaultTierMode applies when neither the server nor the job sets one.
const DefaultTierMode = TiersStrict

var tierModeNames = []TierMode{TiersStrict, TiersWeighted}

// TierModes returns the valid tier modes in declaration order, as a copy.
func TierModes() []TierMode {
	return slices.Clone(tierModeNames)
}

// Valid reports whether m is a tier mode Vagabond implements.
func (m TierMode) Valid() bool {
	return slices.Contains(tierModeNames, m)
}

// String returns the mode as written in configuration or a job file.
func (m TierMode) String() string {
	return string(m)
}
