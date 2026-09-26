-- -----------------------------------------------------------------------------
-- Registered jobs.
--
-- Registering is several statements in one serializable transaction: read the
-- current version, and only if the job changed, insert the next and point the
-- job at it. Two registers racing conflict and one is retried.
-- -----------------------------------------------------------------------------

-- name: CurrentJobVersion :one
SELECT j.version, j.stopped, v.fingerprint
FROM jobs j
JOIN job_versions v
  ON v.namespace = j.namespace AND v.name = j.name AND v.version = j.version
WHERE j.namespace = @namespace AND j.name = @name;

-- name: InsertJobVersion :exec
INSERT INTO job_versions (namespace, name, version, source, fingerprint, created_at)
VALUES (@namespace, @name, @version, @source, @fingerprint, @created_at);

-- name: PointJobAt :exec
-- Makes version current and the job dispatchable again.
INSERT INTO jobs (namespace, name, version, stopped, updated_at)
VALUES (@namespace, @name, @version, FALSE, @updated_at)
ON CONFLICT (namespace, name) DO UPDATE SET
    version    = EXCLUDED.version,
    stopped    = FALSE,
    updated_at = EXCLUDED.updated_at;

-- name: GetJob :one
SELECT namespace, name, version, stopped, updated_at
FROM jobs
WHERE namespace = @namespace AND name = @name;

-- name: ListJobs :many
SELECT namespace, name, version, stopped, updated_at
FROM jobs
WHERE namespace = @namespace
ORDER BY name;

-- name: GetJobVersion :one
SELECT namespace, name, version, source, fingerprint, created_at
FROM job_versions
WHERE namespace = @namespace AND name = @name AND version = @version;

-- name: ListJobVersions :many
-- Newest first, without the source.
SELECT namespace, name, version, fingerprint, created_at
FROM job_versions
WHERE namespace = @namespace AND name = @name
ORDER BY version DESC;

-- name: StopJob :execrows
UPDATE jobs SET stopped = TRUE, updated_at = @updated_at
WHERE namespace = @namespace AND name = @name;
