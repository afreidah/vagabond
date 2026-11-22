// -------------------------------------------------------------------------------
// Registered Jobs
//
// Author: Alex Freidah
//
// A registered job's standing and versions, the fingerprint that decides
// whether registering again is a change, and the schedule a store keeps for a
// periodic job. Whitespace alone is not a change.
// -------------------------------------------------------------------------------

package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/ptr"
)

// ErrNotFound reports a job no one registered in the namespace.
var ErrNotFound = errors.New("job not registered")

// ErrStopped reports a job that was stopped and cannot be dispatched until it
// is registered again.
var ErrStopped = errors.New("job is stopped")

// Job is a registered job's current standing. Periodic is the schedule its
// current version launches by, as Schedule gives it. Launched is when a
// periodic job last launched, or when it became periodic if it has not
// launched since, and zero for any other job.
type Job struct {
	Namespace string
	Name      string
	Version   int64
	Stopped   bool
	Updated   time.Time
	Periodic  *job.Periodic
	Launched  time.Time
}

// Version is one registered version of a job.
type Version struct {
	Namespace   string
	Name        string
	Version     int64
	Source      []byte
	Fingerprint string
	Created     time.Time
}

// Fingerprint identifies source by its formatted form, so reformatting a file
// does not register a new version.
func Fingerprint(source []byte) string {
	sum := sha256.Sum256(hclwrite.Format(source))

	return hex.EncodeToString(sum[:])
}

// Schedule returns the schedule a store keeps for a periodic block: its crons,
// time zone and overlap rule with the defaults filled in, or nil when there is
// no block or it is disabled.
func Schedule(p *job.Periodic) *job.Periodic {
	if !p.Active() {
		return nil
	}

	return &job.Periodic{
		Crons:           slices.Clone(p.Crons),
		TimeZone:        new(ptr.DerefOr(p.TimeZone, "UTC")),
		ProhibitOverlap: new(ptr.Deref(p.ProhibitOverlap)),
	}
}
