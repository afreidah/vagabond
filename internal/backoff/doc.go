// Package backoff is the one schedule Vagabond's retry loops wait on: a delay
// that doubles from a first wait to a ceiling, every wait capped, the first
// included, and a sleep that ends as soon as its caller gives up.
//
// Status polls, reroutes, store writes, provider releases, serialization
// retries, agent reconnects and agent accepts all wait this way. One
// definition keeps their schedules from drifting apart, as a hand-rolled
// copy that forgot to cap its first wait once did.
package backoff
