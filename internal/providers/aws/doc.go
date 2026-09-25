// Package aws dispatches function tasks to AWS Lambda.
//
// Invokes a function the operator deployed; Vagabond does not create one. The
// invocation is synchronous, so the answer, the tail of the log, and what AWS
// billed all come back from Submit.
//
// Everything AWS-specific stops here. depguard forbids importing a cloud SDK
// anywhere else in the tree.
package aws
