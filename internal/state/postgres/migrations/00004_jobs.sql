-- Registered jobs and their versions, and the dispatch each execution belongs to.
--
-- jobs holds each job's current version and whether it is stopped. job_versions
-- keeps every version's source as written, and the fingerprint of its formatted
-- form that decides whether a register is a change. executions.dispatch_id
-- groups one run of a job; '' on rows written before it existed.
--
-- Outside a transaction, as 00002 is, so CockroachDB applies each schema change
-- on its own.

-- +goose NO TRANSACTION

-- +goose Up
CREATE TABLE jobs (
    namespace  TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    version    BIGINT      NOT NULL,
    stopped    BOOLEAN     NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (namespace, name)
);

CREATE TABLE job_versions (
    namespace   TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    version     BIGINT      NOT NULL,
    source      TEXT        NOT NULL,
    fingerprint TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (namespace, name, version)
);

ALTER TABLE executions ADD COLUMN dispatch_id TEXT NOT NULL DEFAULT '';

CREATE INDEX executions_job ON executions (namespace, job, created_at);

-- +goose Down
DROP INDEX executions_job;

ALTER TABLE executions DROP COLUMN dispatch_id;

DROP TABLE job_versions;

DROP TABLE jobs;
