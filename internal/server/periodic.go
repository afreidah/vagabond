// -------------------------------------------------------------------------------
// Periodic Launches
//
// Author: Alex Freidah
//
// Every server launches periodic jobs, and the store makes each launch happen
// once: a server launches a job only after moving its last launch forward from
// the time it read. A pass launches every job whose next launch has come, then
// sleeps until the earliest next one, or a minute at most so jobs registered
// through other servers are seen. A register or stop on this server wakes it
// at once. Launches missed while no server ran are made once, when one
// returns.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/afreidah/vagabond/internal/dispatch"
	"github.com/afreidah/vagabond/internal/jobs"
	"github.com/afreidah/vagabond/internal/ptr"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

const (
	launchInterval = time.Minute // the longest a pass waits, to see jobs registered elsewhere
	launchTask     = "launching periodic jobs"
)

// -------------------------------------------------------------------------
// PASSES
// -------------------------------------------------------------------------

// launchPass launches every periodic job that is due, and returns when the
// next pass should run: the earliest next launch, a minute away at most. An
// error or a panic is logged, and the next pass runs as any other.
func (s *Server) launchPass(ctx context.Context) time.Time {
	next := s.now().Add(launchInterval)

	s.tick(ctx, launchTask, func(ctx context.Context) error {
		earliest, err := s.launchDue(ctx)
		if !earliest.IsZero() && earliest.Before(next) {
			next = earliest
		}

		return err
	})

	return next
}

// launchDue launches every periodic job whose next launch has come, and
// returns the earliest next launch of any job, zero when there is none. A job
// that fails to launch does not hold up the others.
func (s *Server) launchDue(ctx context.Context) (time.Time, error) {
	periodic, err := s.jobs.PeriodicJobs(ctx)
	if err != nil {
		return time.Time{}, err
	}

	now := s.now()

	var (
		earliest time.Time
		errs     []error
	)

	for _, j := range periodic {
		next, err := s.launchIfDue(ctx, j, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("job %q in namespace %q: %w", j.Name, j.Namespace, err))
		}

		if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
			earliest = next
		}
	}

	return earliest, errors.Join(errs...)
}

// launchIfDue makes j's launch when it has come, and returns when j next
// launches. After a launch, or a failed one, that counts from now, so a launch
// that keeps failing waits for the next match rather than retrying at once.
func (s *Server) launchIfDue(ctx context.Context, j *jobs.Job, now time.Time) (time.Time, error) {
	due, err := j.Periodic.Next(j.Launched)
	if err != nil || due.After(now) {
		return due, err
	}

	launchErr := s.launchScheduled(ctx, j, now)
	next, err := j.Periodic.Next(now)

	return next, errors.Join(launchErr, err)
}

// -------------------------------------------------------------------------
// LAUNCHING
// -------------------------------------------------------------------------

// launchScheduled makes j's launch if this server claims it first. When the
// schedule prohibits overlap and a dispatch of j is still running, the launch
// is skipped and still counts as made.
func (s *Server) launchScheduled(ctx context.Context, j *jobs.Job, now time.Time) error {
	claimed, err := s.jobs.ClaimLaunch(ctx, j, now)
	if err != nil || !claimed {
		return err
	}

	logger := s.logger.With("job", j.Name, "namespace", j.Namespace, "version", j.Version)

	if ptr.Deref(j.Periodic.ProhibitOverlap) {
		running, err := s.executions.RunningDispatch(ctx, j.Namespace, j.Name)
		if err != nil {
			return err
		}

		if running {
			logger.InfoContext(ctx, "periodic launch skipped", "reason", "a dispatch of the job is still running")

			return nil
		}
	}

	// The version that was claimed, not whatever is current by now
	version, err := s.jobs.Version(ctx, j.Namespace, j.Name, j.Version)
	if err != nil {
		return err
	}

	spec, err := loadVersion(version, nil)
	if err != nil {
		return err
	}

	started, err := s.launch(ctx, dispatch.Origin{Namespace: j.Namespace, JobVersion: j.Version}, spec, nil)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "periodic launch", "dispatch", started.DispatchID)

	return nil
}

// wakeLaunches makes the next periodic pass run at once. A wake already
// waiting covers this one, so it never blocks.
func (s *Server) wakeLaunches() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
