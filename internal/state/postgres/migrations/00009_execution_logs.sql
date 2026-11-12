-- An execution's whole output, in chunks apart from its record.
--
-- Chunk seq holds bytes seq*256 KiB onward of the output, so a byte offset
-- names its chunk. Past the server's ceiling the oldest chunks are deleted, and
-- logs_start is then the first byte still stored; positions keep counting from
-- the first byte the task printed. Keeping output off the record keeps it out
-- of every read of executions, which the CLI polls each second.
--
-- logs_at is when the output was captured, null until then. Rows written before
-- this table held at most 64 KiB, so each moves to a single chunk 0, captured
-- when it ended. logs_truncated keeps its meaning: the copy is not all of it.
--
-- Outside a transaction, as 00008 is, so CockroachDB applies each schema change
-- on its own.

-- +goose NO TRANSACTION

-- +goose Up
CREATE TABLE execution_logs (
    execution_id TEXT   NOT NULL,
    seq          BIGINT NOT NULL,
    data         BYTEA  NOT NULL,
    PRIMARY KEY (execution_id, seq)
);

INSERT INTO execution_logs (execution_id, seq, data)
SELECT id, 0, logs FROM executions WHERE logs IS NOT NULL AND octet_length(logs) > 0;

ALTER TABLE executions ADD COLUMN logs_start BIGINT NOT NULL DEFAULT 0;

ALTER TABLE executions ADD COLUMN logs_at TIMESTAMPTZ;

UPDATE executions SET logs_at = COALESCE(ended_at, updated_at) WHERE duration_ms IS NOT NULL;

ALTER TABLE executions DROP COLUMN logs;

-- +goose Down
ALTER TABLE executions ADD COLUMN logs BYTEA;

UPDATE executions SET logs = (
    SELECT data FROM execution_logs WHERE execution_id = executions.id AND seq = 0
);

ALTER TABLE executions DROP COLUMN logs_at;

ALTER TABLE executions DROP COLUMN logs_start;

DROP TABLE execution_logs;
