-- Periodic jobs: each job's schedule, and when each periodic job last launched.
--
-- crons, time_zone and prohibit_overlap are the current version's periodic
-- block, with crons empty when it has none or the block is disabled. A
-- periodic_launches row holds when the job last launched, or when it became
-- periodic if it has not launched since; a launch is claimed by moving it
-- forward. dispatches_running answers whether any dispatch of a job is still
-- running, for a schedule that prohibits overlap.
--
-- Outside a transaction, as 00012 is, so CockroachDB applies each schema change
-- on its own.

-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE jobs ADD COLUMN crons TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE jobs ADD COLUMN time_zone TEXT NOT NULL DEFAULT '';

ALTER TABLE jobs ADD COLUMN prohibit_overlap BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE periodic_launches (
    namespace   TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    launched_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (namespace, name)
);

CREATE INDEX dispatches_running ON dispatches (namespace, job) WHERE state = 'running';

-- +goose Down
DROP INDEX dispatches_running;

DROP TABLE periodic_launches;

ALTER TABLE jobs DROP COLUMN prohibit_overlap;

ALTER TABLE jobs DROP COLUMN time_zone;

ALTER TABLE jobs DROP COLUMN crons;
