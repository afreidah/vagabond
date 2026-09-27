// -------------------------------------------------------------------------------
// Memory Job Store
//
// Author: Alex Freidah
//
// Registered jobs in maps, for a server started with -dev and for tests. The
// same rules as the Postgres store: a new version only when the formatted
// source changed, and a stopped job revived by registering it again. Lost with
// the process.
// -------------------------------------------------------------------------------

package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/afreidah/vagabond/internal/execution"
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

// Jobs holds registered jobs and every version of each. Executions are read
// from the execution store the jobs run against.
type Jobs struct {
	mu         sync.Mutex
	jobs       map[jobKey]jobs.Job
	versions   map[jobKey][]jobs.Version
	executions *Executions
}

// NewJobs returns an empty store reading executions from executions.
func NewJobs(executions *Executions) *Jobs {
	return &Jobs{
		jobs:       make(map[jobKey]jobs.Job),
		versions:   make(map[jobKey][]jobs.Version),
		executions: executions,
	}
}

// -------------------------------------------------------------------------
// WRITES
// -------------------------------------------------------------------------

// Register stores source as the job's next version when it differs from the
// current one, and reports the job's version afterwards and whether it changed.
// Registering a stopped job always makes a new version.
func (s *Jobs) Register(
	_ context.Context, namespace, name string, source []byte, now time.Time,
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
	s.jobs[key] = jobs.Job{Namespace: namespace, Name: name, Version: version, Updated: now}

	return version, true, nil
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

// JobExecutions returns a job's most recent executions, newest first, at most
// limit of them.
func (s *Jobs) JobExecutions(_ context.Context, namespace, job string, limit int) ([]*execution.Record, error) {
	return s.executions.jobExecutions(namespace, job, limit), nil
}

// latest returns the newest version of a job the store holds.
func (s *Jobs) latest(key jobKey) jobs.Version {
	stored := s.versions[key]
	if len(stored) == 0 {
		return jobs.Version{}
	}

	return stored[len(stored)-1]
}
