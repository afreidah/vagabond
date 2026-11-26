//go:build containerd || firecracker

// -------------------------------------------------------------------------------
// Executor Test Fixtures
//
// Author: Alex Freidah
//
// What the containerd and firecracker executor tests share: the image they
// run, small and with a shell, a workload of it at the size a microVM boots
// at, and a check of what a workload's network reaches.
// -------------------------------------------------------------------------------

package executor

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/afreidah/vagabond/internal/agentrpc"
	"github.com/afreidah/vagabond/internal/execution"
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

// reachScript listens on the node and returns a script that prints
// "internet" when it can connect to 1.1.1.1 and "node" when it can connect to
// the node's address on its default route, which is a private address on
// every machine the tests run on.
func reachScript(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(t.Context(), "tcp", ":0")
	if err != nil {
		t.Fatalf("listening on the node: %v", err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	// Dialing UDP sends nothing, but picks the address the default route leaves from
	var d net.Dialer

	route, err := d.DialContext(t.Context(), "udp", "1.1.1.1:53")
	if err != nil {
		t.Fatalf("finding the node's address: %v", err)
	}

	defer func() { _ = route.Close() }()

	node := route.LocalAddr().(*net.UDPAddr).IP
	port := ln.Addr().(*net.TCPAddr).Port

	return fmt.Sprintf("nc -z -w 3 1.1.1.1 443 && echo internet; nc -z -w 3 %s %d && echo node; true", node, port)
}

// reached reads a workload's output from logs, an executor's Logs, and reports
// whether it reached the internet and the node.
func reached(t *testing.T, logs func(context.Context, string, int64, execution.LogWriter) error, id string) (internet, node bool) {
	t.Helper()

	var out strings.Builder

	if err := logs(t.Context(), id, 0, &execution.PlacedWriter{Writer: &out}); err != nil {
		t.Fatalf("Logs() = %v", err)
	}

	return strings.Contains(out.String(), "internet"), strings.Contains(out.String(), "node")
}
