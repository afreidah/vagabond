// -------------------------------------------------------------------------------
// Leaked Job Cleanup
//
// Author: Alex Freidah
//
// Cloud Run has no ad-hoc run: every execution needs a Job resource that
// exists until something deletes it. A process that dies between running and
// deleting leaves one behind, and enough of those eventually exhaust a
// per-region quota and stop dispatch entirely.
//
// Not one of the five interface methods, because nothing else has this
// problem. It is a Cloud Run tax, so it is paid in the Cloud Run plugin, and
// whatever owns the execution lifecycle decides when to call it.
// -------------------------------------------------------------------------------

package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// sweepGrace is how old a job must be before the sweep will touch it.
//
// Generous on purpose. A job younger than this may belong to a run in
// progress, and deleting that would destroy the execution and the exit code
// along with it. Cloud Run's own task timeout maximum is a day, so anything
// older than that cannot still be running.
const sweepGrace = 25 * time.Hour

// -------------------------------------------------------------------------
// SWEEPING
// -------------------------------------------------------------------------

// Sweep deletes jobs this plugin created and abandoned.
//
// Only jobs carrying our prefix, so nothing an operator created by hand in the
// same project is at risk. Only jobs past the grace period, so a run in flight
// is never destroyed by housekeeping.
//
// Failures are collected rather than returned on the first one: a single job
// that cannot be deleted should not leave the rest leaked.
func (p *Provider) Sweep(ctx context.Context, now time.Time) (int, error) {
	jobs, err := p.listJobs(ctx)
	if err != nil {
		return 0, err
	}

	var (
		swept    int
		failures []error
	)

	for _, j := range jobs {
		if !p.isLeaked(&j, now) {
			continue
		}

		if err := p.call(ctx, http.MethodDelete,
			p.runURL+"/v2/"+j.Name, nil, nil); err != nil && !isNotFound(err) {
			failures = append(failures, fmt.Errorf("deleting %s: %w", j.Name, err))

			continue
		}

		swept++
	}

	return swept, errors.Join(failures...)
}

// isLeaked reports whether a job is ours and old enough to remove.
func (p *Provider) isLeaked(j *runJob, now time.Time) bool {
	name := j.Name[strings.LastIndex(j.Name, "/")+1:]
	if !strings.HasPrefix(name, namePrefix) {
		return false
	}

	created := parseTime(j.CreateTime)
	if created.IsZero() {
		// A job whose age cannot be read is left alone. Guessing wrong here
		// deletes a running execution, and the cost of leaving one behind is
		// a row in a quota nobody is near.
		return false
	}

	return now.Sub(created) > sweepGrace
}

// -------------------------------------------------------------------------
// LISTING
// -------------------------------------------------------------------------

// runJob is a job resource, reduced to what the sweep needs.
type runJob struct {
	Name       string `json:"name"`
	CreateTime string `json:"createTime"`
}

// listJobs pages through every job in the project and region.
func (p *Provider) listJobs(ctx context.Context) ([]runJob, error) {
	var (
		jobs  []runJob
		token string
	)

	for {
		url := p.cfg.jobsURL(p.runURL) + "?pageSize=100"
		if token != "" {
			url += "&pageToken=" + token
		}

		var out struct {
			Jobs          []runJob `json:"jobs"`
			NextPageToken string   `json:"nextPageToken"`
		}

		if err := p.call(ctx, http.MethodGet, url, nil, &out); err != nil {
			return nil, err
		}

		jobs = append(jobs, out.Jobs...)

		if out.NextPageToken == "" {
			return jobs, nil
		}

		token = out.NextPageToken
	}
}
