-- Execution records, one per attempt.
--
-- previous_id links a rerouted attempt to the one before it, '' on a task's
-- first. failure is the class of error that ended an attempt without an answer,
-- '' when none did. The result columns are null until one is recorded.
--
-- logs is BYTEA because task output need not be valid UTF-8, which TEXT
-- refuses. CockroachDB reads it as BYTES.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE executions (
    id             TEXT        NOT NULL,
    namespace      TEXT        NOT NULL,
    job            TEXT        NOT NULL,
    job_version    BIGINT      NOT NULL,
    task           TEXT        NOT NULL,
    provider       TEXT        NOT NULL,
    attempt        BIGINT      NOT NULL,
    previous_id    TEXT        NOT NULL,
    state          TEXT        NOT NULL,
    provider_id    TEXT        NOT NULL,
    failure        TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    started_at     TIMESTAMPTZ,
    ended_at       TIMESTAMPTZ,
    updated_at     TIMESTAMPTZ NOT NULL,
    exit_code      BIGINT,
    duration_ms    BIGINT,
    billed_cpu     BIGINT,
    billed_memory  BIGINT,
    billed_ms      BIGINT,
    logs           BYTEA,
    logs_truncated BOOLEAN     NOT NULL,
    PRIMARY KEY (id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE executions;
-- +goose StatementEnd
