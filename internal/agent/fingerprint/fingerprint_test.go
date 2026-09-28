// -------------------------------------------------------------------------------
// Fingerprint Tests
//
// Author: Alex Freidah
//
// Hosts written as in-memory filesystems: a bare host, a Nomad allocation's
// limits, an ancestor tighter than the agent's own cgroup, operator caps, and a
// host still on cgroup v1.
// -------------------------------------------------------------------------------

package fingerprint

import (
	"runtime"
	"testing"
	"testing/fstest"
)

// host is a 16 GiB machine whose agent runs in the named cgroup, with the
// given cgroup files.
func host(cgroup string, files map[string]string) fstest.MapFS {
	root := fstest.MapFS{
		"proc/meminfo":     {Data: []byte("MemTotal:       16777216 kB\nMemFree: 1 kB\n")},
		"proc/self/cgroup": {Data: []byte("0::" + cgroup + "\n")},
	}

	for name, data := range files {
		root[name] = &fstest.MapFile{Data: []byte(data)}
	}

	return root
}

func TestTake(t *testing.T) {
	hostCPU := int64(runtime.NumCPU()) * 1000

	tests := []struct {
		name       string
		root       fstest.MapFS
		caps       Caps
		wantCPU    int64
		wantMemory int64
	}{
		{
			name:       "bare host",
			root:       host("/system.slice/vagabond-agent.service", nil),
			wantCPU:    hostCPU,
			wantMemory: 16384,
		},
		{
			name: "nomad allocation limits",
			root: host("/nomad.slice/alloc.agent.scope", map[string]string{
				"sys/fs/cgroup/nomad.slice/alloc.agent.scope/cpu.max":    "150000 100000\n",
				"sys/fs/cgroup/nomad.slice/alloc.agent.scope/memory.max": "2147483648\n",
			}),
			wantCPU:    min(hostCPU, 1500),
			wantMemory: 2048,
		},
		{
			name: "an ancestor is tighter",
			root: host("/parent/child", map[string]string{
				"sys/fs/cgroup/parent/child/memory.max": "max\n",
				"sys/fs/cgroup/parent/memory.max":       "1073741824\n",
			}),
			wantCPU:    hostCPU,
			wantMemory: 1024,
		},
		{
			name:       "operator caps",
			root:       host("/system.slice/vagabond-agent.service", nil),
			caps:       Caps{CPU: 500, Memory: 512},
			wantCPU:    min(hostCPU, 500),
			wantMemory: 512,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, err := Take(tt.root, tt.caps)
			if err != nil {
				t.Fatalf("Take() = %v", err)
			}

			if node.CPU != tt.wantCPU || node.Memory != tt.wantMemory {
				t.Errorf("Take() = %d millicores, %d MiB; want %d, %d", node.CPU, node.Memory, tt.wantCPU, tt.wantMemory)
			}
		})
	}
}

// A host on cgroup v1 is refused rather than advertised without limits.
func TestTake_CgroupV1(t *testing.T) {
	root := host("", nil)
	root["proc/self/cgroup"] = &fstest.MapFile{Data: []byte("12:memory:/user.slice\n1:name=systemd:/user.slice\n")}

	if _, err := Take(root, Caps{}); err == nil {
		t.Error("Take() accepted a cgroup v1 host")
	}
}
