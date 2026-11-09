// -------------------------------------------------------------------------------
// Version
//
// Author: Alex Freidah
//
// The version string the CLI and the server report, from values injected at
// link time or, failing those, the revision the Go toolchain embeds.
// -------------------------------------------------------------------------------

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

// String returns the version for the CLI to print. The commit falls back to the
// one the Go toolchain embeds, so a plain `go build` is still traceable.
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

// vcsRevision reads the revision the Go toolchain records for a build from a
// checkout, or empty when the build carried none, as in a test binary.
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
