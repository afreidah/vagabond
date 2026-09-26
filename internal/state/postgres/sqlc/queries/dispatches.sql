-- -----------------------------------------------------------------------------
-- Dispatch records.
--
-- Finishing changes only a dispatch still running, so a late or repeated
-- finish cannot overwrite how a run ended.
-- -----------------------------------------------------------------------------

-- name: CreateDispatch :exec
INSERT INTO dispatches (dispatch_id, namespace, job, job_version, state, error, created_at, ended_at)
VALUES (@dispatch_id, @namespace, @job, @job_version, @state, '', @created_at, NULL);

-- name: FinishDispatch :execrows
UPDATE dispatches SET state = @state, error = @error, ended_at = @ended_at
WHERE dispatch_id = @dispatch_id AND state = 'running';

-- name: GetDispatch :one
SELECT * FROM dispatches WHERE dispatch_id = @dispatch_id;
