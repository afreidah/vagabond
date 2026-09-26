-- Dispatch leases, and the declared shape of each execution.
--
-- A running dispatch names its owner and holds a lease the owner renews; a
-- server takes over a dispatch whose lease has lapsed. Rows written before
-- this carry an expired lease, so a dispatch left running is taken over.
-- tasks is how many tasks the job declares, 0 on older rows.
--
-- executions.cpu and memory are what the task declared, so an execution
-- resumed after a restart can be charged without its job. 0 on older rows.
--
-- Outside a transaction, as 00002 is, so CockroachDB applies each schema change
-- on its own.

-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE dispatches ADD COLUMN tasks BIGINT NOT NULL DEFAULT 0;

ALTER TABLE dispatches ADD COLUMN owner TEXT NOT NULL DEFAULT '';

ALTER TABLE dispatches ADD COLUMN lease_until TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01 00:00:00+00';

CREATE INDEX dispatches_lease ON dispatches (state, lease_until);

ALTER TABLE executions ADD COLUMN cpu BIGINT NOT NULL DEFAULT 0;

ALTER TABLE executions ADD COLUMN memory BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE executions DROP COLUMN memory;

ALTER TABLE executions DROP COLUMN cpu;

DROP INDEX dispatches_lease;

ALTER TABLE dispatches DROP COLUMN lease_until;

ALTER TABLE dispatches DROP COLUMN owner;

ALTER TABLE dispatches DROP COLUMN tasks;
