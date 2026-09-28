// -------------------------------------------------------------------------------
// Node Fingerprint
//
// Author: Alex Freidah
//
// What the agent may use, not what the host has. Capacity is the smallest of
// the host's resources, the limits on the agent's own cgroup and every cgroup
// above it, and what the operator capped it at. In a Nomad allocation or a
// systemd unit with limits, the cgroup is what makes the answer true.
// -------------------------------------------------------------------------------

package fingerprint

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"runtime"
	"strconv"
	"strings"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Node is what an agent reports about its host. CPU is millicores and Memory
// MiB, matching job.Resources. Cgroup is the agent's own cgroup v2 path, which
// its workloads are placed under.
type Node struct {
	Architecture string
	CPU          int64
	Memory       int64
	Cgroup       string
}

// Caps are operator limits on what the agent may use; zero means none.
type Caps struct {
	CPU    int64
	Memory int64
}

// -------------------------------------------------------------------------
// TAKING
// -------------------------------------------------------------------------

// Take reads the host through root, a filesystem rooted at "/", and returns
// what the agent may use.
func Take(root fs.FS, caps Caps) (Node, error) {
	memory, err := hostMemory(root)
	if err != nil {
		return Node{}, err
	}

	cgroup, err := ownCgroup(root)
	if err != nil {
		return Node{}, err
	}

	node := Node{
		Architecture: runtime.GOARCH,
		CPU:          int64(runtime.NumCPU()) * 1000,
		Memory:       memory,
		Cgroup:       cgroup,
	}

	limitCPU, limitMemory, err := cgroupLimits(root, cgroup)
	if err != nil {
		return Node{}, err
	}

	node.CPU = smallest(node.CPU, limitCPU, caps.CPU)
	node.Memory = smallest(node.Memory, limitMemory, caps.Memory)

	return node, nil
}

// smallest returns the least of the values that are set, zero meaning unset.
func smallest(first int64, rest ...int64) int64 {
	least := first

	for _, v := range rest {
		if v > 0 && v < least {
			least = v
		}
	}

	return least
}

// -------------------------------------------------------------------------
// HOST
// -------------------------------------------------------------------------

// hostMemory reads MemTotal from /proc/meminfo, in MiB.
func hostMemory(root fs.FS) (int64, error) {
	data, err := fs.ReadFile(root, "proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("reading host memory: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))

	for scanner.Scan() {
		value, ok := strings.CutPrefix(scanner.Text(), "MemTotal:")
		if !ok {
			continue
		}

		kib, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "kB")), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing MemTotal: %w", err)
		}

		return kib / 1024, nil
	}

	return 0, errors.New("no MemTotal in /proc/meminfo")
}

// -------------------------------------------------------------------------
// CGROUPS
// -------------------------------------------------------------------------

// ownCgroup reads the agent's cgroup v2 path from /proc/self/cgroup. A host
// still on cgroup v1 is refused, since limits could not be read or enforced.
func ownCgroup(root fs.FS) (string, error) {
	data, err := fs.ReadFile(root, "proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("reading the agent's cgroup: %w", err)
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if cgroup, ok := strings.CutPrefix(line, "0::"); ok {
			return cgroup, nil
		}
	}

	return "", errors.New("the host is not on cgroup v2, which the agent requires")
}

// cgroupLimits returns the tightest CPU and memory limits on cgroup and every
// cgroup above it, zero where none is set.
func cgroupLimits(root fs.FS, cgroup string) (cpu, memory int64, err error) {
	for dir := cgroup; ; dir = path.Dir(dir) {
		base := path.Join("sys/fs/cgroup", dir)

		c, err := cpuMax(root, base)
		if err != nil {
			return 0, 0, err
		}

		m, err := memoryMax(root, base)
		if err != nil {
			return 0, 0, err
		}

		cpu, memory = tighter(cpu, c), tighter(memory, m)

		if dir == "/" || dir == "." {
			return cpu, memory, nil
		}
	}
}

// tighter returns the smaller of two limits, zero meaning unlimited.
func tighter(a, b int64) int64 {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	default:
		return min(a, b)
	}
}

// cpuMax reads a cgroup's cpu.max, "quota period" in microseconds or "max", as
// millicores. A missing file is no limit.
func cpuMax(root fs.FS, dir string) (int64, error) {
	fields, ok, err := readFields(root, path.Join(dir, "cpu.max"))
	if err != nil || !ok || len(fields) != 2 || fields[0] == "max" {
		return 0, err
	}

	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s/cpu.max: %w", dir, err)
	}

	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || period == 0 {
		return 0, fmt.Errorf("parsing %s/cpu.max period: %q", dir, fields[1])
	}

	return quota * 1000 / period, nil
}

// memoryMax reads a cgroup's memory.max, bytes or "max", as MiB. A missing file
// is no limit.
func memoryMax(root fs.FS, dir string) (int64, error) {
	fields, ok, err := readFields(root, path.Join(dir, "memory.max"))
	if err != nil || !ok || len(fields) != 1 || fields[0] == "max" {
		return 0, err
	}

	limit, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s/memory.max: %w", dir, err)
	}

	return limit / (1 << 20), nil
}

// readFields reads a one-line cgroup file as whitespace-separated fields, and
// reports false for a file that does not exist.
func readFields(root fs.FS, name string) ([]string, bool, error) {
	data, err := fs.ReadFile(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", name, err)
	}

	return strings.Fields(string(data)), true, nil
}
