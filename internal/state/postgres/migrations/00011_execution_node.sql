-- The agent node an execution runs on, for a provider made of nodes.
--
-- Empty for every other provider, and for every row written before this
-- column.
--
-- Outside a transaction, as 00007 is, so CockroachDB applies the schema change
-- on its own.

-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE executions ADD COLUMN node TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE executions DROP COLUMN node;
