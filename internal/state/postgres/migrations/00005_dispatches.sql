-- One row per run of a job, above its executions. A run refused before any
-- task was submitted has no executions, so this row is its only trace. error
-- says why an unanswered run got no answer; '' otherwise.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE dispatches (
    dispatch_id TEXT        NOT NULL,
    namespace   TEXT        NOT NULL,
    job         TEXT        NOT NULL,
    job_version BIGINT      NOT NULL,
    state       TEXT        NOT NULL,
    error       TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    ended_at    TIMESTAMPTZ,
    PRIMARY KEY (dispatch_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE dispatches;
-- +goose StatementEnd
