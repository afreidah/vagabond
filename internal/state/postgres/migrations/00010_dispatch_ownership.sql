-- A dispatch's lease covers its executions until all of them are released.
--
-- epoch counts claims, so a write checked against the lease is refused after
-- any claim. done_at is when every execution finished through release; a
-- claim takes only dispatches not yet done. cancel_requested_at is a cancel
-- for the owner to act on. An execution's timeout_ms is its declared timeout,
-- what a run that never answered is charged for; 0 on existing rows.
--
-- An ended dispatch with an unreleased execution is left open for a claim to
-- finish. Executions without a dispatch are marked released. A reservation's
-- cpu and memory go: the execution's runner settles it from the record.

-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE dispatches ADD COLUMN epoch BIGINT NOT NULL DEFAULT 1;

ALTER TABLE dispatches ADD COLUMN cancel_requested_at TIMESTAMPTZ;

ALTER TABLE dispatches ADD COLUMN done_at TIMESTAMPTZ;

ALTER TABLE executions ADD COLUMN timeout_ms BIGINT NOT NULL DEFAULT 0;

ALTER TABLE quota_reservations DROP COLUMN cpu;

ALTER TABLE quota_reservations DROP COLUMN memory;

UPDATE dispatches SET done_at = COALESCE(ended_at, created_at)
WHERE state <> 'running' AND NOT EXISTS (
    SELECT 1 FROM executions
    WHERE executions.dispatch_id = dispatches.dispatch_id AND executions.released_at IS NULL
);

UPDATE executions SET released_at = updated_at WHERE dispatch_id = '' AND released_at IS NULL;

DROP INDEX dispatches_lease;

CREATE INDEX dispatches_lease ON dispatches (lease_until) WHERE done_at IS NULL;

DROP INDEX executions_unreleased;

-- +goose Down
CREATE INDEX executions_unreleased ON executions (updated_at) WHERE released_at IS NULL;

DROP INDEX dispatches_lease;

CREATE INDEX dispatches_lease ON dispatches (state, lease_until);

ALTER TABLE quota_reservations ADD COLUMN memory BIGINT NOT NULL DEFAULT 0;

ALTER TABLE quota_reservations ADD COLUMN cpu BIGINT NOT NULL DEFAULT 0;

ALTER TABLE executions DROP COLUMN timeout_ms;

ALTER TABLE dispatches DROP COLUMN done_at;

ALTER TABLE dispatches DROP COLUMN cancel_requested_at;

ALTER TABLE dispatches DROP COLUMN epoch;
