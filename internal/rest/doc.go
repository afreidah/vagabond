// Package rest is the one request path for the JSON APIs Vagabond calls: its
// own server's, for the CLI, and Cloud Run's, Code Engine's and IBM IAM's,
// for the providers. A call is encoded, sent and read the same way
// everywhere; what a refusal means is left to the caller, since that is
// where the callers differ.
//
// A failure is one of three kinds: a *Refusal, a non-2xx answer with its
// status and the start of its body; ErrUnreachable, no whole answer at all;
// or anything else, a request that could not be built or an answer that
// could not be decoded, which is the caller's own fault.
package rest
