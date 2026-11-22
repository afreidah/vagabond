// -------------------------------------------------------------------------------
// Periodic Launch Tests
//
// Author: Alex Freidah
//
// A pass launches a due job once, however many servers share the store and
// however many launches were missed. It skips a launch that would overlap a
// running dispatch, and never waits longer than a minute. Each server's clock
// is set by hand, and its launches are counted from what it logs.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/mock/gomock"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/execution"
	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/state/memory"
	"github.com/afreidah/vagabond/internal/testkit"
)

// -------------------------------------------------------------------------
// FIXTURES
// -------------------------------------------------------------------------

// minutelyJob launches every minute on the synchronous function provider.
const minutelyJob = `
job "minutely" {
  type = "batch"

  periodic {
    crons = ["* * * * *"]
  }

  routing {
    providers = ["fn"]
  }

  task "greet" {
    driver = "function"

    config {
      function = "hello"
    }
  }
}
`

// registeredAt is when the tests register their jobs, on a whole minute.
var registeredAt = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

// logged counts the messages a server logs, safe for the dispatches that log
// beside a pass.
type logged struct {
	mu       sync.Mutex
	messages map[string]int
}

// Enabled takes every level, so no message goes uncounted.
func (l *logged) Enabled(context.Context, slog.Level) bool {
	return true
}

// Handle counts the record's message and drops the rest of it.
func (l *logged) Handle(_ context.Context, r slog.Record) error { //nolint:gocritic // slog.Handler fixes the signature
	l.mu.Lock()
	defer l.mu.Unlock()

	l.messages[r.Message]++

	return nil
}

// WithAttrs counts into the same place, since attributes are not counted.
func (l *logged) WithAttrs([]slog.Attr) slog.Handler {
	return l
}

// WithGroup counts into the same place, since groups are not counted.
func (l *logged) WithGroup(string) slog.Handler {
	return l
}

// count returns how many times msg was logged.
func (l *logged) count(msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.messages[msg]
}

// periodicServer builds a server named owner over store and executions, whose
// clock reads now, and returns it with what it logs.
func periodicServer(
	t *testing.T, store serverJobs, executions *memory.Executions, owner string, now time.Time,
) (*Server, *logged) {
	t.Helper()

	reg, led := fixtures(t)
	log := &logged{messages: make(map[string]int)}

	s := New(reg, led, executions, store, memory.NewProviders(), slog.New(log), WithOwner(owner))
	s.now = func() time.Time { return now }

	return s, log
}

// periodicHarness serves a server over an empty memory job store whose clock
// reads now.
func periodicHarness(t *testing.T, now time.Time) (*harness, *Server) {
	t.Helper()

	s, _ := periodicServer(t, memory.NewJobs(), memory.NewExecutions(), "server:a", now)

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	return &harness{url: srv.URL}, s
}

// registerAt registers source in store at now with the periodic block it
// declares, as registering through the API would.
func registerAt(t *testing.T, store *memory.Jobs, source string, now time.Time) {
	t.Helper()

	parsed, diags := jobs.ForRegister("job", []byte(source))
	if diags.HasErrors() {
		t.Fatalf("loading the job: %s", diags.Error())
	}

	j := &parsed.Spec.Jobs[0]

	if _, _, err := store.Register(t.Context(), "default", j.Name, []byte(source), j.Periodic, now); err != nil {
		t.Fatalf("Register() = %v", err)
	}
}

// launchedAt reads when job minutely last launched.
func launchedAt(t *testing.T, store *memory.Jobs) time.Time {
	t.Helper()

	j, err := store.Job(t.Context(), "default", "minutely")
	if err != nil {
		t.Fatalf("Job() = %v", err)
	}

	return j.Launched
}

// pass runs one launch pass on s and returns when the next launch is,
// failing the test on an error.
func pass(t *testing.T, s *Server) time.Time {
	t.Helper()

	earliest, err := s.launchDue(t.Context())
	if err != nil {
		t.Fatalf("launchDue() = %v", err)
	}

	return earliest
}

// -------------------------------------------------------------------------
// PASSES
// -------------------------------------------------------------------------

// A pass launches a job whose next launch has come, once: a second pass at
// the same moment finds the next launch still ahead and names it.
func TestLaunchDue_LaunchesOnce(t *testing.T) {
	store := memory.NewJobs()
	registerAt(t, store, minutelyJob, registeredAt)

	now := registeredAt.Add(90 * time.Second)
	s, log := periodicServer(t, store, memory.NewExecutions(), "server:a", now)

	for range 2 {
		if got, want := pass(t, s), registeredAt.Add(2*time.Minute); !got.Equal(want) {
			t.Errorf("next launch = %s, want %s", got, want)
		}
	}

	if got := log.count("periodic launch"); got != 1 {
		t.Errorf("launches = %d, want 1", got)
	}

	if got := launchedAt(t, store); !got.Equal(now) {
		t.Errorf("Launched = %s, want %s", got, now)
	}
}

// A launch not yet due is not made, and the pass names when it will be.
func TestLaunchDue_NotYetDue(t *testing.T) {
	store := memory.NewJobs()
	registerAt(t, store, minutelyJob, registeredAt)

	s, log := periodicServer(t, store, memory.NewExecutions(), "server:a", registeredAt.Add(30*time.Second))

	if got, want := pass(t, s), registeredAt.Add(time.Minute); !got.Equal(want) {
		t.Errorf("next launch = %s, want %s", got, want)
	}

	if got := log.count("periodic launch"); got != 0 {
		t.Errorf("launches = %d, want 0", got)
	}
}

// A server down across ten launches makes one when it returns, and the next
// counts from then.
func TestLaunchDue_MissedLaunchesMadeOnce(t *testing.T) {
	store := memory.NewJobs()
	registerAt(t, store, minutelyJob, registeredAt)

	s, log := periodicServer(t, store, memory.NewExecutions(), "server:a", registeredAt.Add(10*time.Minute+30*time.Second))

	if got, want := pass(t, s), registeredAt.Add(11*time.Minute); !got.Equal(want) {
		t.Errorf("next launch = %s, want %s", got, want)
	}

	if got := log.count("periodic launch"); got != 1 {
		t.Errorf("launches = %d, want 1", got)
	}
}

// A job whose stored version no longer loads is reported by name, and the
// other jobs still launch.
func TestLaunchDue_OneFailureDoesNotHoldUpOthers(t *testing.T) {
	store := memory.NewJobs()
	registerAt(t, store, minutelyJob, registeredAt)

	broken := &job.Periodic{Crons: []string{"* * * * *"}}
	if _, _, err := store.Register(t.Context(), "default", "broken", []byte(`job "broken" {}`), broken, registeredAt); err != nil {
		t.Fatalf("Register(broken) = %v", err)
	}

	s, log := periodicServer(t, store, memory.NewExecutions(), "server:a", registeredAt.Add(90*time.Second))

	if _, err := s.launchDue(t.Context()); err == nil || !strings.Contains(err.Error(), `"broken"`) {
		t.Errorf("launchDue() = %v, want an error naming broken", err)
	}

	if got := log.count("periodic launch"); got != 1 {
		t.Errorf("launches = %d, want minutely's 1", got)
	}
}

// Two servers on one store, passing at the same moment, launch the job once
// between them.
func TestLaunchDue_TwoServersLaunchOnce(t *testing.T) {
	store, executions := memory.NewJobs(), memory.NewExecutions()
	registerAt(t, store, minutelyJob, registeredAt)

	now := registeredAt.Add(90 * time.Second)
	a, aLog := periodicServer(t, store, executions, "server:a", now)
	b, bLog := periodicServer(t, store, executions, "server:b", now)

	var wg sync.WaitGroup

	for _, s := range []*Server{a, b} {
		wg.Go(func() {
			if _, err := s.launchDue(t.Context()); err != nil {
				t.Errorf("launchDue() = %v", err)
			}
		})
	}

	wg.Wait()

	if got := aLog.count("periodic launch") + bLog.count("periodic launch"); got != 1 {
		t.Errorf("launches between both servers = %d, want 1", got)
	}
}

// With overlap prohibited, a launch while a dispatch of the job is running is
// skipped and still counts as made. With overlap allowed, it is made.
func TestLaunchDue_Overlap(t *testing.T) {
	tests := []struct {
		name     string
		prohibit bool
		made     int
		skipped  int
	}{
		{name: "prohibited", prohibit: true, made: 0, skipped: 1},
		{name: "allowed", prohibit: false, made: 1, skipped: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := minutelyJob
			if tt.prohibit {
				source = strings.Replace(minutelyJob, `crons = ["* * * * *"]`,
					"crons = [\"* * * * *\"]\n    prohibit_overlap = true", 1)
			}

			store, executions := memory.NewJobs(), memory.NewExecutions()
			registerAt(t, store, source, registeredAt)

			now := registeredAt.Add(90 * time.Second)

			// A run of the job started by hand is still going
			err := executions.CreateDispatch(t.Context(), &execution.Dispatch{
				ID: testkit.NewID(t), Namespace: "default", Job: "minutely", Tasks: 1,
				State: execution.DispatchRunning, Owner: "server:c", Epoch: 1,
				LeaseUntil: now.Add(time.Hour), Created: now,
			})
			if err != nil {
				t.Fatalf("CreateDispatch() = %v", err)
			}

			s, log := periodicServer(t, store, executions, "server:a", now)
			pass(t, s)

			if made, skipped := log.count("periodic launch"), log.count("periodic launch skipped"); made != tt.made || skipped != tt.skipped {
				t.Errorf("made %d, skipped %d; want %d, %d", made, skipped, tt.made, tt.skipped)
			}

			if got := launchedAt(t, store); !got.Equal(now) {
				t.Errorf("Launched = %s, want %s either way", got, now)
			}
		})
	}
}

// A pass waits for the earliest next launch, but never longer than a minute,
// even when the store cannot be read, which is logged.
func TestLaunchPass_WaitsAMinuteAtMost(t *testing.T) {
	now := registeredAt.Add(90 * time.Second)

	t.Run("next launch sooner", func(t *testing.T) {
		store := memory.NewJobs()
		registerAt(t, store, minutelyJob, registeredAt)

		s, _ := periodicServer(t, store, memory.NewExecutions(), "server:a", now)

		if got, want := s.launchPass(t.Context()), registeredAt.Add(2*time.Minute); !got.Equal(want) {
			t.Errorf("launchPass() = %s, want %s", got, want)
		}
	})

	t.Run("next launch a day away", func(t *testing.T) {
		store := memory.NewJobs()
		registerAt(t, store, strings.Replace(minutelyJob, `"* * * * *"`, `"@daily"`, 1), registeredAt)

		s, _ := periodicServer(t, store, memory.NewExecutions(), "server:a", now)

		if got := s.launchPass(t.Context()); !got.Equal(now.Add(launchInterval)) {
			t.Errorf("launchPass() = %s, want a minute on", got)
		}
	})

	t.Run("store unreadable", func(t *testing.T) {
		store := NewMockserverJobs(gomock.NewController(t))
		store.EXPECT().PeriodicJobs(gomock.Any()).Return(nil, errors.New("connection refused"))

		s, log := periodicServer(t, store, memory.NewExecutions(), "server:a", now)

		if got := s.launchPass(t.Context()); !got.Equal(now.Add(launchInterval)) {
			t.Errorf("launchPass() = %s, want a minute on", got)
		}

		if got := log.count(launchTask); got != 1 {
			t.Errorf("failures logged = %d, want 1", got)
		}
	})
}

// A wake makes repeat's next call at once rather than at the time the last
// call asked for, an hour away here, and repeat returns when its context ends.
func TestRepeat_WakeCallsAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	wake := make(chan struct{}, 1)
	calls := make(chan struct{}, 2)
	done := make(chan struct{})

	go func() {
		defer close(done)

		(&Server{}).repeat(ctx, wake, time.Now(), func(context.Context) time.Time {
			calls <- struct{}{}

			return time.Now().Add(time.Hour)
		})
	}()

	<-calls

	wake <- struct{}{}

	select {
	case <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("a wake did not bring the next call forward")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("repeat did not return when its context ended")
	}
}

// -------------------------------------------------------------------------
// ROUTES
// -------------------------------------------------------------------------

// Registering a periodic job answers with its first launch and wakes the
// launcher, and its status carries the schedule. Stopping it wakes the
// launcher again, and its status then carries none.
func TestRegister_PeriodicJob(t *testing.T) {
	h, s := periodicHarness(t, registeredAt.Add(30*time.Second))
	want := registeredAt.Add(time.Minute)

	var resp api.RegisterResponse

	if status := h.call(t, http.MethodPost, "/v1/jobs", api.RegisterRequest{Source: minutelyJob}, &resp); status != http.StatusOK {
		t.Fatalf("register status = %d", status)
	}

	if resp.NextLaunch == nil || !resp.NextLaunch.Equal(want) {
		t.Errorf("NextLaunch = %v, want %s", resp.NextLaunch, want)
	}

	woken(t, s, "registering")

	var status api.JobStatus

	h.call(t, http.MethodGet, "/v1/job/minutely", nil, &status)

	if diff := cmp.Diff(&api.Periodic{Crons: []string{"* * * * *"}, TimeZone: "UTC"}, status.Periodic); diff != "" {
		t.Errorf("Periodic (-want +got):\n%s", diff)
	}

	if status.NextLaunch == nil || !status.NextLaunch.Equal(want) {
		t.Errorf("status NextLaunch = %v, want %s", status.NextLaunch, want)
	}

	var stopped api.Job

	h.call(t, http.MethodDelete, "/v1/job/minutely", nil, &stopped)
	woken(t, s, "stopping")

	if stopped.Periodic != nil || stopped.NextLaunch != nil {
		t.Errorf("stopped job = %+v, want no schedule", stopped)
	}
}

// Running a periodic job file is refused and points to registering it, since
// only its schedule launches it.
func TestRun_PeriodicJobRefused(t *testing.T) {
	h, _ := periodicHarness(t, registeredAt)

	var resp api.Error

	status := h.call(t, http.MethodPost, "/v1/jobs/run", api.RunRequest{Source: minutelyJob}, &resp)

	if status != http.StatusBadRequest || !strings.Contains(resp.Error, "vagabond job register") {
		t.Errorf("status = %d, error %q; want 400 pointing to job register", status, resp.Error)
	}
}

// Planning a periodic job file says when it would first launch if it were
// registered now.
func TestPlan_PeriodicJobFile(t *testing.T) {
	h, _ := periodicHarness(t, registeredAt.Add(30*time.Second))

	var plan api.Plan

	if status := h.call(t, http.MethodPost, "/v1/jobs/plan", api.PlanRequest{Source: minutelyJob}, &plan); status != http.StatusOK {
		t.Fatalf("plan status = %d", status)
	}

	if want := registeredAt.Add(time.Minute); plan.NextLaunch == nil || !plan.NextLaunch.Equal(want) {
		t.Errorf("NextLaunch = %v, want %s", plan.NextLaunch, want)
	}
}

// woken fails the test unless what sent s's launcher a wake.
func woken(t *testing.T, s *Server, what string) {
	t.Helper()

	select {
	case <-s.wake:
	default:
		t.Errorf("%s did not wake the launcher", what)
	}
}
