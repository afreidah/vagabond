// -------------------------------------------------------------------------------
// Admitter - the optional provider-specific half of admission
//
// Author: Alex Freidah
//
// Shared admission rules are a pure function over a capability snapshot and a
// quota snapshot. Some limits only a provider understands, though, and this is
// how it contributes them without admission calling into it.
//
// Declared here rather than beside Provider for two reasons. It returns a
// Rejection, which is this package's vocabulary, so declaring it in the plugin
// package would make plugin import scheduler while scheduler already imports
// plugin. And it is the consumer's view of a collaborator, which is the pattern
// everything except the Provider boundary itself follows.
// -------------------------------------------------------------------------------

package scheduler

import (
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// Admitter is implemented by providers with limits the capability model cannot
// express.
//
// Optional. A provider whose constraints are fully described by its
// Capabilities implements nothing, and the shared checkers handle it. The
// dispatcher type-asserts for this rather than requiring every plugin to carry
// a method returning nil.
//
// Implementations must be pure. Admission runs across every configured provider
// on a plan, which has to stay fast and free of side effects, and this is the
// one place a plugin could quietly break that. It receives a snapshot rather
// than a context precisely so that reaching the network is awkward enough to
// notice in review.
type Admitter interface {
	// AdmitTask reports why this provider cannot run the task, or nil if it
	// can. Reasons come from the shared vocabulary so that plan output stays
	// uniform however many providers are configured.
	AdmitTask(task job.Task, caps *plugin.Capabilities) *Rejection
}
