// -------------------------------------------------------------------------------
// Version Tests
//
// Author: Alex Freidah
//
// A version string is the first thing asked for in a bug report, so the cases
// that matter are the ones where the build carried no information: it must
// still say something rather than nothing.
// -------------------------------------------------------------------------------

package version

import (
	"strings"
	"testing"
)

func TestString_IncludesVersion(t *testing.T) {
	if !strings.Contains(String(), Version) {
		t.Errorf("String() = %q, want it to contain %q", String(), Version)
	}
}

// A binary built without ldflags reports itself as a development build. That is
// honest; a hardcoded version committed and forgotten would claim to be a
// release.
func TestString_DefaultIsDevelopmentBuild(t *testing.T) {
	if Version != "dev" {
		t.Errorf("Version = %q, want the uninjected default to be dev", Version)
	}
}

func TestString_WithCommit(t *testing.T) {
	original := Commit
	t.Cleanup(func() { Commit = original })

	Commit = "abcdef123456"

	got := String()
	if !strings.Contains(got, "abcdef123456") {
		t.Errorf("String() = %q, want it to contain the commit", got)
	}
}

func TestShortRevision(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "full hash is trimmed",
			input: "0123456789abcdef0123456789abcdef01234567",
			want:  "0123456789ab",
		},
		{
			name:  "short hash is left alone",
			input: "abc123",
			want:  "abc123",
		},
		{
			name:  "empty stays empty",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortRevision(tt.input); got != tt.want {
				t.Errorf("shortRevision(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// A test binary carries no VCS revision, so this must return empty rather than
// panic or report something invented.
func TestVCSRevision_ToleratesMissingBuildInfo(t *testing.T) {
	if rev := vcsRevision(); len(rev) > 12 {
		t.Errorf("vcsRevision() = %q, want an empty or short revision", rev)
	}
}
