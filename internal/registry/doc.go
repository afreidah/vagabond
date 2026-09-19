// Package registry turns configuration into the providers admission reads.
//
// It sits where the layers meet: it constructs provider plugins, holds their
// latest capability and quota snapshots, and hands admission the values it
// needs. That is why it is here rather than in the scheduler, which depguard
// forbids from importing a provider implementation at all.
//
// Nothing in admission may reach a network, so something has to own freshness.
// This does. A snapshot is gathered when the registry is built or refreshed, and
// admission reads whatever was gathered last.
package registry
