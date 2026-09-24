-- Quota usage and reservations.
--
-- quota_usage holds what settled executions cost, one row per provider pool per
-- period, where period is the calendar key the pool resets on: '2026-09'
-- monthly, '2026-09-22' daily. Rollover needs no job, because a new period is a
-- key nothing has written to yet.
--
-- quota_reservations holds what running executions are expected to cost, one
-- row per pool the provider meters, zero amounts included so settling knows the
-- period each was charged in. cpu and memory are the task's declared shape,
-- kept so the reaper can price a run it did not dispatch.
--
-- No sequences, triggers or foreign keys anywhere in this schema. All three
-- differ or are absent on CockroachDB, which speaks the same wire protocol.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE quota_usage (
    provider   TEXT        NOT NULL,
    pool       TEXT        NOT NULL,
    period     TEXT        NOT NULL,
    used       BIGINT      NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (provider, pool, period)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE quota_reservations (
    execution_id TEXT        NOT NULL,
    provider     TEXT        NOT NULL,
    pool         TEXT        NOT NULL,
    period       TEXT        NOT NULL,
    amount       BIGINT      NOT NULL,
    cpu          BIGINT      NOT NULL,
    memory       BIGINT      NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (execution_id, pool)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX quota_reservations_pool ON quota_reservations (provider, pool, period);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE quota_reservations;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE quota_usage;
-- +goose StatementEnd
