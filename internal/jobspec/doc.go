// Package jobspec reads .vagabond.hcl files into the specification types.
//
// Parsing is separate from the types it produces, following Nomad's split
// between jobspec2 and the packages holding its job structures. The types
// describe what a job is; this package describes how one is written down, and
// the two change for different reasons.
//
// Everything here returns hcl.Diagnostics rather than error. A diagnostic
// carries a source range, so a problem can be reported against the line the
// author wrote, and several problems can be reported in one run rather than one
// per attempt. That is the difference between a validator people keep running
// and one they stop.
package jobspec
