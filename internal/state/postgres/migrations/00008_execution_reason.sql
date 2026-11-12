-- Why the platform ended an execution's workload, in its own words.
--
-- Empty when the process exited on its own or the platform gave no reason,
-- which every row written before this column is.
--
-- Outside a transaction, as 00007 is, so CockroachDB applies the schema change
-- on its own.

-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE executions ADD COLUMN reason TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE executions DROP COLUMN reason;
