-- -----------------------------------------------------------------------------
-- Registered jobs.
--
-- Registering is several statements in one serializable transaction: read the
-- current version, and only if the job changed, insert the next and point the
-- job at it, and record the time when it has just become periodic. Two
-- registers racing conflict and one is retried.
--
-- A job's reads carry its last launch, which only a periodic job has.
-- -----------------------------------------------------------------------------

-- name: CurrentJobVersion :one
SELECT j.version, j.stopped, j.crons, v.fingerprint
FROM jobs j
JOIN job_versions v
  ON v.namespace = j.namespace AND v.name = j.name AND v.version = j.version
WHERE j.namespace = @namespace AND j.name = @name;

-- name: InsertJobVersion :exec
INSERT INTO job_versions (namespace, name, version, source, fingerprint, created_at)
VALUES (@namespace, @name, @version, @source, @fingerprint, @created_at);

-- name: PointJobAt :exec
-- Makes version current with its schedule, and the job dispatchable again.
INSERT INTO jobs (namespace, name, version, stopped, updated_at, crons, time_zone, prohibit_overlap)
VALUES (@namespace, @name, @version, FALSE, @updated_at, @crons, @time_zone, @prohibit_overlap)
ON CONFLICT (namespace, name) DO UPDATE SET
    version          = EXCLUDED.version,
    stopped          = FALSE,
    updated_at       = EXCLUDED.updated_at,
    crons            = EXCLUDED.crons,
    time_zone        = EXCLUDED.time_zone,
    prohibit_overlap = EXCLUDED.prohibit_overlap;

-- name: ResetLaunch :exec
-- Records when a job became periodic, so its first launch waits for the next
-- time a cron matches.
INSERT INTO periodic_launches (namespace, name, launched_at)
VALUES (@namespace, @name, @launched_at)
ON CONFLICT (namespace, name) DO UPDATE SET launched_at = EXCLUDED.launched_at;

-- name: GetJob :one
SELECT j.namespace, j.name, j.version, j.stopped, j.updated_at,
       j.crons, j.time_zone, j.prohibit_overlap, p.launched_at
FROM jobs j
LEFT JOIN periodic_launches p ON p.namespace = j.namespace AND p.name = j.name
WHERE j.namespace = @namespace AND j.name = @name;

-- name: ListJobs :many
SELECT j.namespace, j.name, j.version, j.stopped, j.updated_at,
       j.crons, j.time_zone, j.prohibit_overlap, p.launched_at
FROM jobs j
LEFT JOIN periodic_launches p ON p.namespace = j.namespace AND p.name = j.name
WHERE j.namespace = @namespace
ORDER BY j.name;

-- name: ListPeriodicJobs :many
-- Every job in any namespace that launches on a schedule.
SELECT j.namespace, j.name, j.version, j.stopped, j.updated_at,
       j.crons, j.time_zone, j.prohibit_overlap, p.launched_at
FROM jobs j
LEFT JOIN periodic_launches p ON p.namespace = j.namespace AND p.name = j.name
WHERE NOT j.stopped AND j.crons <> '{}'
ORDER BY j.namespace, j.name;

-- name: ClaimLaunch :execrows
-- Moves a job's last launch from the time the caller read to now, while the
-- job is still running the version the caller read. No row changes when
-- another server launched it first, or the job changed or stopped since.
UPDATE periodic_launches p SET launched_at = @now
WHERE p.namespace = @namespace AND p.name = @name AND p.launched_at = @last
  AND EXISTS (
    SELECT 1 FROM jobs j
    WHERE j.namespace = p.namespace AND j.name = p.name
      AND j.version = @version AND NOT j.stopped
  );

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
