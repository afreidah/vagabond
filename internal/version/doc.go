// Package version reports the build a binary was produced from.
//
// The values are injected with -ldflags at build time. A binary built without
// them reports itself as a development build, since a committed version string
// that is never bumped claims to be a release it is not.
package version
