// Package job holds Vagabond's workload model: the provider-independent
// description of what a caller wants run.
//
// The model is deliberately independent of two things it sits between. It does
// not describe HCL, which is the format a job happens to arrive in, and it does
// not describe any cloud, which is a routing destination chosen later. A task
// states what it needs; admission decides who can satisfy it; the scheduler
// decides where; the provider plugin decides how.
//
// Driver names live here because they are part of the workload model: a task
// asks for an execution contract, not for a company. Provider names do not live
// here, and a type in this package that mentions one is a bug rather than a
// convenience.
package job
