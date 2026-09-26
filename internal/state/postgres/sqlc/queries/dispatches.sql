-- -----------------------------------------------------------------------------
-- Dispatch records.
--
-- Finishing and renewing change only a dispatch still running and still held
-- by the caller, so a late finish, or one from an owner whose lease was taken
-- over, cannot overwrite how a run ended.
-- -----------------------------------------------------------------------------

-- name: CreateDispatch :exec
INSERT INTO dispatches (
    dispatch_id, namespace, job, job_version, state, error, created_at, ended_at,
    tasks, owner, lease_until
) VALUES (
    @dispatch_id, @namespace, @job, @job_version, @state, '', @created_at, NULL,
    @tasks, @owner, @lease_until
);

-- name: FinishDispatch :execrows
UPDATE dispatches SET state = @state, error = @error, ended_at = @ended_at
WHERE dispatch_id = @dispatch_id AND state = 'running' AND owner = @owner;

-- name: RenewDispatch :execrows
UPDATE dispatches SET lease_until = @lease_until
WHERE dispatch_id = @dispatch_id AND state = 'running' AND owner = @owner;

-- name: ClaimDispatches :many
-- Takes over every running dispatch whose lease lapsed before now. One
-- statement, so two servers claiming at once never both get the same one.
UPDATE dispatches SET owner = @owner, lease_until = @lease_until
WHERE state = 'running' AND lease_until < @now::timestamptz
RETURNING *;

-- name: GetDispatch :one
SELECT * FROM dispatches WHERE dispatch_id = @dispatch_id;
