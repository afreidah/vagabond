//go:build containerd || firecracker

// -------------------------------------------------------------------------------
// Executor Test Fixtures
//
// Author: Alex Freidah
//
// What the containerd and firecracker executor tests share: the image they
// run, small and with a shell, and a workload of it at the size a microVM
// boots at.
// -------------------------------------------------------------------------------

package executor

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/afreidah/vagabond/internal/agentrpc"
)

// testImage is small and has a shell.
const testImage = "alpine:3.20"

// workload runs command from the test image at a quarter core and 128 MiB.
func workload(timeout time.Duration, command ...string) *agentrpc.Workload {
	return &agentrpc.Workload{
		Image:     testImage,
		Command:   command,
		Resources: &agentrpc.Resources{Cpu: 250, Memory: 128},
		Timeout:   durationpb.New(timeout),
	}
}
