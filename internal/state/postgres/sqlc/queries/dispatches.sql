-- -----------------------------------------------------------------------------
-- Dispatch records.
--
-- The owner's writes are conditional on its lease, owner and epoch. The lease
-- is held until the dispatch is done.
-- -----------------------------------------------------------------------------

-- name: CreateDispatch :exec
INSERT INTO dispatches (
    dispatch_id, namespace, job, job_version, state, error, created_at, ended_at,
    tasks, owner, lease_until, epoch
) VALUES (
    @dispatch_id, @namespace, @job, @job_version, @state, '', @created_at, NULL,
    @tasks, @owner, @lease_until, @epoch
);

-- name: FinishDispatch :execrows
-- Records the verdict, once.
UPDATE dispatches SET state = @state, error = @error, ended_at = @ended_at
WHERE dispatch_id = @dispatch_id AND state = 'running' AND owner = @owner AND epoch = @epoch;

-- name: CloseDispatch :execrows
-- Records that every execution of the dispatch finished all its steps.
UPDATE dispatches SET done_at = @done_at
WHERE dispatch_id = @dispatch_id AND done_at IS NULL AND owner = @owner AND epoch = @epoch;

-- name: RenewDispatch :one
-- Extends the lease and returns any cancel requested since.
UPDATE dispatches SET lease_until = @lease_until
WHERE dispatch_id = @dispatch_id AND done_at IS NULL AND owner = @owner AND epoch = @epoch
RETURNING cancel_requested_at;

-- name: RequestCancel :execrows
-- Asks whoever holds the dispatch to cancel it; the first request stands.
UPDATE dispatches SET cancel_requested_at = COALESCE(cancel_requested_at, @requested_at)
WHERE dispatch_id = @dispatch_id AND done_at IS NULL;

-- name: ClaimDispatches :many
-- Takes every open dispatch whose lease lapsed, except the caller's own, under
-- the next epoch. One statement, so no two servers claim the same one.
UPDATE dispatches SET owner = @owner, lease_until = @lease_until, epoch = epoch + 1
WHERE done_at IS NULL AND lease_until < @now::timestamptz
  AND NOT (dispatch_id = ANY(@skip::text[]))
RETURNING *;

-- name: GetDispatch :one
SELECT * FROM dispatches WHERE dispatch_id = @dispatch_id;
