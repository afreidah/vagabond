// -------------------------------------------------------------------------------
// Version
//
// Author: Alex Freidah
//
// The version reported by the CLI and the control plane. Set at link time so
// that a built binary reports what it actually is, rather than whatever string
// was last committed.
// -------------------------------------------------------------------------------

// Package version reports the build the binary was produced from.
//
// The values here are injected with -ldflags at build time. A binary built
// without them reports itself as a development build, which is honest: a
// hardcoded version that is committed and then forgotten is worse than no
// version, because it claims to be a release.
package version

import (
	"fmt"
	"runtime/debug"
)

// Injected at link time. See the Makefile's build target.
var (
	Version = "dev"
	Commit  = ""
)

// String returns a human-readable version for the CLI to print.
//
// The commit falls back to the one the Go toolchain embeds in a VCS-built
// binary, so `go build` alone still produces something traceable without the
// Makefile.
func String() string {
	commit := Commit
	if commit == "" {
		commit = vcsRevision()
	}

	if commit == "" {
		return Version
	}

	return fmt.Sprintf("%s (%s)", Version, commit)
}

// vcsRevision reads the revision the Go toolchain records for a binary built
// from a checkout. Returns an empty string when the build carried no VCS
// information, such as inside a test binary.
func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return shortRevision(setting.Value)
		}
	}

	return ""
}

// shortRevision trims a full git hash to the length people actually read.
func shortRevision(rev string) string {
	const short = 12

	if len(rev) <= short {
		return rev
	}

	return rev[:short]
}
