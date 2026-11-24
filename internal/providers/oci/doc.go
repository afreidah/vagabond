// Package oci dispatches function tasks to OCI Functions.
//
// Invokes a function the operator deployed; Vagabond does not create one. The
// invocation is synchronous, so the function's reply comes back from Submit,
// billed at the memory the function is configured with for as long as it ran.
//
// Everything OCI-specific stops here. depguard forbids importing a cloud SDK
// anywhere else in the tree.
package oci
