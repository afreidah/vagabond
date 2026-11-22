//go:build integration

// -------------------------------------------------------------------------------
// Periodic Launches Across Servers
//
// Author: Alex Freidah
//
// Servers on one database, each with its own registry, as separate processes
// would have. A yearly job is registered as if on the last day of last year,
// so this year's launch was missed and every server's first pass, at startup,
// finds it due at once, without waiting for a real minute to pass.
// -------------------------------------------------------------------------------

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/server"
	"github.com/afreidah/vagabond/internal/testkit"
)

// settle is long enough for every running server's first pass to have
// finished, which nothing outside the server can see.
const settle = 2 * time.Second

// yearlyJob is functionJob launching on the first of every year.
var yearlyJob = strings.Replace(functionJob, `type = "batch"`,
	"type = \"batch\"\n\n  periodic {\n    crons = [\"@yearly\"]\n  }", 1)

// Two servers starting together on one database make the missed launch once
// between them, and a server started afterwards does not make it again. The
// next launch is next year's.
func TestPeriodic_LaunchedOnceAcrossServers(t *testing.T) {
	first := newHarness(t, functionProvider)
	year := time.Now().UTC().Year()

	// Registered as if on the last day of last year, so this year's launch is due
	_, _, err := first.store.Register(context.Background(), job.DefaultNamespace, "hello", []byte(yearlyJob),
		&job.Periodic{Crons: []string{"@yearly"}}, time.Date(year-1, 12, 31, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	second := &harness{t: t, registry: providers(t, functionProvider), store: first.store}

	a := first.serve(server.WithOwner("server-a"))
	second.serve(server.WithOwner("server-b"))

	launches := func() int {
		status, err := a.JobStatus(context.Background(), "", "hello")
		if err != nil {
			t.Fatalf("JobStatus() = %v", err)
		}

		return len(status.Executions)
	}

	testkit.Eventually(t, runTimeout, "the missed launch", func() bool { return launches() > 0 })
	time.Sleep(settle)

	if got := launches(); got != 1 {
		t.Fatalf("launches by two servers = %d, want 1", got)
	}

	// A server started later, as one restarting would be
	late := &harness{t: t, registry: providers(t, functionProvider), store: first.store}
	late.serve(server.WithOwner("server-c"))
	time.Sleep(settle)

	if got := launches(); got != 1 {
		t.Errorf("launches after a third server started = %d, want 1", got)
	}

	status, err := a.JobStatus(context.Background(), "", "hello")
	if err != nil {
		t.Fatalf("JobStatus() = %v", err)
	}

	if want := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC); status.NextLaunch == nil || !status.NextLaunch.Equal(want) {
		t.Errorf("NextLaunch = %v, want %s", status.NextLaunch, want)
	}
}
