// -------------------------------------------------------------------------------
// Registered Job Persistence
//
// Author: Alex Freidah
//
// Registering is a read and up to two writes in one serializable transaction,
// retried on conflict, so two registers of the same job cannot take the same
// version number.
// -------------------------------------------------------------------------------

package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/afreidah/vagabond/internal/jobs"
	db "github.com/afreidah/vagabond/internal/state/postgres/sqlc"
)

// Register stores source as the job's next version when it differs from the
// current one, and reports the job's version afterwards and whether it changed.
// Registering a stopped job always makes a new version, as Nomad does.
func (s *Store) Register(
	ctx context.Context, namespace, name string, source []byte, now time.Time,
) (int64, bool, error) {
	fingerprint := jobs.Fingerprint(source)

	var (
		version int64
		changed bool
	)

	err := s.serializable(ctx, func(q *db.Queries) error {
		current, err := q.CurrentJobVersion(ctx, db.CurrentJobVersionParams{Namespace: namespace, Name: name})

		switch {
		case errors.Is(err, pgx.ErrNoRows):
			current = db.CurrentJobVersionRow{}
		case err != nil:
			return fmt.Errorf("read job %s: %w", name, err)
		case !current.Stopped && current.Fingerprint == fingerprint:
			version, changed = current.Version, false

			return nil
		}

		version, changed = current.Version+1, true

		err = q.InsertJobVersion(ctx, db.InsertJobVersionParams{
			Namespace:   namespace,
			Name:        name,
			Version:     version,
			Source:      string(source),
			Fingerprint: fingerprint,
			CreatedAt:   now,
		})
		if err != nil {
			return fmt.Errorf("insert job %s version %d: %w", name, version, err)
		}

		err = q.PointJobAt(ctx, db.PointJobAtParams{
			Namespace: namespace, Name: name, Version: version, UpdatedAt: now,
		})
		if err != nil {
			return fmt.Errorf("point job %s at version %d: %w", name, version, err)
		}

		return nil
	})

	return version, changed, err
}

// Job returns a registered job's standing, stopped or not.
func (s *Store) Job(ctx context.Context, namespace, name string) (*jobs.Job, error) {
	row, err := s.queries.GetJob(ctx, db.GetJobParams{Namespace: namespace, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q in namespace %q", jobs.ErrNotFound, name, namespace)
	}

	if err != nil {
		return nil, fmt.Errorf("get job %s: %w", name, err)
	}

	return jobOf(&row), nil
}

// Jobs returns every job registered in namespace, by name.
func (s *Store) Jobs(ctx context.Context, namespace string) ([]*jobs.Job, error) {
	rows, err := s.queries.ListJobs(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	out := make([]*jobs.Job, 0, len(rows))
	for i := range rows {
		out = append(out, jobOf(&rows[i]))
	}

	return out, nil
}

// Version returns one version of a job, source included.
func (s *Store) Version(ctx context.Context, namespace, name string, version int64) (*jobs.Version, error) {
	row, err := s.queries.GetJobVersion(ctx, db.GetJobVersionParams{
		Namespace: namespace, Name: name, Version: version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q version %d", jobs.ErrNotFound, name, version)
	}

	if err != nil {
		return nil, fmt.Errorf("get job %s version %d: %w", name, version, err)
	}

	return &jobs.Version{
		Namespace:   row.Namespace,
		Name:        row.Name,
		Version:     row.Version,
		Source:      []byte(row.Source),
		Fingerprint: row.Fingerprint,
		Created:     row.CreatedAt,
	}, nil
}

// Versions returns every version of a job, newest first, without source.
func (s *Store) Versions(ctx context.Context, namespace, name string) ([]*jobs.Version, error) {
	rows, err := s.queries.ListJobVersions(ctx, db.ListJobVersionsParams{Namespace: namespace, Name: name})
	if err != nil {
		return nil, fmt.Errorf("list versions of %s: %w", name, err)
	}

	out := make([]*jobs.Version, 0, len(rows))

	for i := range rows {
		out = append(out, &jobs.Version{
			Namespace:   rows[i].Namespace,
			Name:        rows[i].Name,
			Version:     rows[i].Version,
			Fingerprint: rows[i].Fingerprint,
			Created:     rows[i].CreatedAt,
		})
	}

	return out, nil
}

// Stop deregisters a job. Its versions are kept; registering it again revives
// it.
func (s *Store) Stop(ctx context.Context, namespace, name string, now time.Time) error {
	return s.serializable(ctx, func(q *db.Queries) error {
		stopped, err := q.StopJob(ctx, db.StopJobParams{Namespace: namespace, Name: name, UpdatedAt: now})
		if err != nil {
			return fmt.Errorf("stop job %s: %w", name, err)
		}

		if stopped == 0 {
			return fmt.Errorf("%w: %q in namespace %q", jobs.ErrNotFound, name, namespace)
		}

		return nil
	})
}

// jobOf maps a jobs row.
func jobOf(row *db.Job) *jobs.Job {
	return &jobs.Job{
		Namespace: row.Namespace,
		Name:      row.Name,
		Version:   row.Version,
		Stopped:   row.Stopped,
		Updated:   row.UpdatedAt,
	}
}
