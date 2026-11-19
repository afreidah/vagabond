// -------------------------------------------------------------------------------
// Process Translation Tests
//
// Author: Alex Freidah
//
// The process a container's spec runs, as the firecracker executor hands it to
// the daemon: its arguments, environment, working directory and user.
// -------------------------------------------------------------------------------

package executor

import (
	"slices"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// A spec's process reaches the daemon whole, its environment as a map that
// keeps the last value of a variable set twice, as a shell would.
func TestProcessOf(t *testing.T) {
	got := processOf(&specs.Process{
		Args: []string{"/bin/sh", "-c", "id"},
		Env:  []string{"PATH=/usr/bin", "QUERY=a=b", "PATH=/bin"},
		Cwd:  "/work",
		User: specs.User{UID: 1000, GID: 1000, AdditionalGids: []uint32{1001, 1002}},
	})

	if !slices.Equal(got.GetArgs(), []string{"/bin/sh", "-c", "id"}) || got.GetWorkingDir() != "/work" {
		t.Errorf("processOf() = %+v", got)
	}

	if env := got.GetEnv(); env["PATH"] != "/bin" || env["QUERY"] != "a=b" {
		t.Errorf("env = %v, want the last PATH and the whole QUERY", env)
	}

	if got.GetUid() != 1000 || got.GetGid() != 1000 || !slices.Equal(got.GetAdditionalGids(), []uint32{1001, 1002}) {
		t.Errorf("user = %d:%d %v, want 1000:1000 [1001 1002]", got.GetUid(), got.GetGid(), got.GetAdditionalGids())
	}
}
