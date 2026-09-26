// -------------------------------------------------------------------------------
// Registered Jobs
//
// Author: Alex Freidah
//
// A registered job's standing and versions, and the fingerprint that decides
// whether registering again is a change. Whitespace alone is not.
// -------------------------------------------------------------------------------

package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

// ErrNotFound reports a job no one registered in the namespace.
var ErrNotFound = errors.New("job not registered")

// ErrStopped reports a job that was stopped and cannot be dispatched until it
// is registered again.
var ErrStopped = errors.New("job is stopped")

// Job is a registered job's current standing.
type Job struct {
	Namespace string
	Name      string
	Version   int64
	Stopped   bool
	Updated   time.Time
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
