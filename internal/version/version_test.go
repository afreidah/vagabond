// -------------------------------------------------------------------------------
// Version Tests
//
// Author: Alex Freidah
//
// The version string with and without injected values, including a build
// that carried no VCS information and must still report something.
// -------------------------------------------------------------------------------

package version

import (
	"strings"
	"testing"
)

// The rendered string always carries the version itself.
func TestString_IncludesVersion(t *testing.T) {
	if !strings.Contains(String(), Version) {
		t.Errorf("String() = %q, want it to contain %q", String(), Version)
	}
}

// A binary built without ldflags reports itself as a development build.
func TestString_DefaultIsDevelopmentBuild(t *testing.T) {
	if Version != "dev" {
		t.Errorf("Version = %q, want the uninjected default to be dev", Version)
	}
}

// An injected commit appears beside the version.
func TestString_WithCommit(t *testing.T) {
	original := Commit
	t.Cleanup(func() { Commit = original })

	Commit = "abcdef123456"

	got := String()
	if !strings.Contains(got, "abcdef123456") {
		t.Errorf("String() = %q, want it to contain the commit", got)
	}
}

// A full hash is cut to twelve characters, and a shorter one is left alone.
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

// A test binary carries no VCS revision, so this returns empty rather than
// panicking or inventing one.
func TestVCSRevision_ToleratesMissingBuildInfo(t *testing.T) {
	if rev := vcsRevision(); len(rev) > 12 {
		t.Errorf("vcsRevision() = %q, want an empty or short revision", rev)
	}
}
