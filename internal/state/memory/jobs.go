// -------------------------------------------------------------------------------
// Memory Job Store
//
// Author: Alex Freidah
//
// Registered jobs in maps, for a server started with -dev and for tests. The
// same rules as the Postgres store: a new version only when the formatted
// source changed, a stopped job revived by registering it again, and a
// periodic job's launch claimed against the version and last launch the
// caller read. Lost with the process.
// -------------------------------------------------------------------------------

package memory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/jobs"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// jobKey identifies a job within the store.
type jobKey struct {
	namespace string
	name      string
}

// Jobs holds registered jobs and every version of each.
type Jobs struct {
	mu       sync.Mutex
	jobs     map[jobKey]jobs.Job
	versions map[jobKey][]jobs.Version
}

// NewJobs returns an empty store.
func NewJobs() *Jobs {
	return &Jobs{
		jobs:     make(map[jobKey]jobs.Job),
		versions: make(map[jobKey][]jobs.Version),
	}
}

// -------------------------------------------------------------------------
// WRITES
// -------------------------------------------------------------------------

// Register stores source as the job's next version when it differs from the
// current one, with the schedule its periodic block gives, and reports the
// job's version afterwards and whether it changed. Registering a stopped job
// always makes a new version.
func (s *Jobs) Register(
	_ context.Context, namespace, name string, source []byte, periodic *job.Periodic, now time.Time,
) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := jobKey{namespace, name}
	fingerprint := jobs.Fingerprint(source)
	current, ok := s.jobs[key]

	if ok && !current.Stopped && s.latest(key).Fingerprint == fingerprint {
		return current.Version, false, nil
	}

	version := current.Version + 1

	s.versions[key] = append(s.versions[key], jobs.Version{
		Namespace: namespace, Name: name, Version: version,
		Source: append([]byte(nil), source...), Fingerprint: fingerprint, Created: now,
	})

	next := jobs.Job{Namespace: namespace, Name: name, Version: version, Updated: now, Periodic: jobs.Schedule(periodic)}

	// A job that has just become periodic waits for the next match after now
	if next.Periodic != nil {
		next.Launched = now
		if !current.Stopped && current.Periodic != nil {
			next.Launched = current.Launched
		}
	}

	s.jobs[key] = next

	return version, true, nil
}

// ClaimLaunch moves j's last launch from j.Launched to now, and reports whether
// it did. It does not when another server launched j first, or j changed
// version or stopped since it was read.
func (s *Jobs) ClaimLaunch(_ context.Context, j *jobs.Job, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := jobKey{j.Namespace, j.Name}

	current, ok := s.jobs[key]
	if !ok || current.Stopped || current.Periodic == nil || current.Version != j.Version ||
		!current.Launched.Equal(j.Launched) {
		return false, nil
	}

	current.Launched = now
	s.jobs[key] = current

	return true, nil
}

// Stop deregisters a job. Its versions are kept; registering it again revives
// it.
func (s *Jobs) Stop(_ context.Context, namespace, name string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := jobKey{namespace, name}

	j, ok := s.jobs[key]
	if !ok {
		return fmt.Errorf("%w: %q in namespace %q", jobs.ErrNotFound, name, namespace)
	}

	j.Stopped, j.Updated = true, now
	s.jobs[key] = j

	return nil
}

// -------------------------------------------------------------------------
// READS
// -------------------------------------------------------------------------

// Job returns a registered job's standing, stopped or not.
func (s *Jobs) Job(_ context.Context, namespace, name string) (*jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	j, ok := s.jobs[jobKey{namespace, name}]
	if !ok {
		return nil, fmt.Errorf("%w: %q in namespace %q", jobs.ErrNotFound, name, namespace)
	}

	return &j, nil
}

// Jobs returns every job registered in namespace, by name.
func (s *Jobs) Jobs(_ context.Context, namespace string) ([]*jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*jobs.Job

	for key := range s.jobs {
		if key.namespace == namespace {
			j := s.jobs[key]
			out = append(out, &j)
		}
	}

	slices.SortFunc(out, func(a, b *jobs.Job) int { return strings.Compare(a.Name, b.Name) })

	return out, nil
}

// PeriodicJobs returns every job in any namespace that launches on a
// schedule, with when each last launched.
func (s *Jobs) PeriodicJobs(_ context.Context) ([]*jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*jobs.Job

	for key := range s.jobs {
		if j := s.jobs[key]; !j.Stopped && j.Periodic != nil {
			out = append(out, &j)
		}
	}

	slices.SortFunc(out, func(a, b *jobs.Job) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
	})

	return out, nil
}

// Version returns one version of a job, source included.
func (s *Jobs) Version(_ context.Context, namespace, name string, version int64) (*jobs.Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, v := range s.versions[jobKey{namespace, name}] {
		if v.Version == version {
			v.Source = append([]byte(nil), v.Source...)

			return &v, nil
		}
	}

	return nil, fmt.Errorf("%w: %q version %d", jobs.ErrNotFound, name, version)
}

// Versions returns every version of a job, newest first, without source.
func (s *Jobs) Versions(_ context.Context, namespace, name string) ([]*jobs.Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := s.versions[jobKey{namespace, name}]
	out := make([]*jobs.Version, 0, len(stored))

	for _, v := range slices.Backward(stored) {
		v.Source = nil
		out = append(out, &v)
	}

	return out, nil
}

// latest returns the newest version of a job the store holds.
func (s *Jobs) latest(key jobKey) jobs.Version {
	stored := s.versions[key]
	if len(stored) == 0 {
		return jobs.Version{}
	}

	return stored[len(stored)-1]
}
