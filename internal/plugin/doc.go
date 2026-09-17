// Package plugin holds the contract every cloud backend implements, and the
// types the scheduler reasons about instead of reasoning about clouds.
//
// The package is named for the boundary rather than for providers, because
// provider.Provider would stutter and because internal/providers holds the
// implementations. Nothing here imports a cloud SDK, and depguard enforces
// that the implementations are the only packages that do.
//
// Capabilities is the load-bearing type. Admission must decide whether a
// provider could run a task without calling that provider: job plan fans out
// across every configured backend and has to stay fast, free, and free of side
// effects. Everything admission needs to know therefore has to be expressible
// here, as data, observed earlier and cached.
package plugin
