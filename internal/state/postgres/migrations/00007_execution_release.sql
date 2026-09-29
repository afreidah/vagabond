-- When what a provider left behind for an execution was cleaned up.
--
-- released_at is null until the release loop, or dispatch after reading the
-- result, has released it. The partial index holds only the rows still waiting,
-- which is all the loop reads.
--
-- Older rows with a recorded result went through dispatch's release already,
-- so they are marked released at their last update. Those without one are the
-- runs that may have left something behind, and the loop works through them.
--
-- Outside a transaction, as 00002 is, so CockroachDB applies each schema change
-- on its own.

-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE executions ADD COLUMN released_at TIMESTAMPTZ;

UPDATE executions SET released_at = updated_at WHERE duration_ms IS NOT NULL;

CREATE INDEX executions_unreleased ON executions (updated_at) WHERE released_at IS NULL;

-- +goose Down
DROP INDEX executions_unreleased;

ALTER TABLE executions DROP COLUMN released_at;
