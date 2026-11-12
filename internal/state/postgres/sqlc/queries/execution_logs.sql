-- -----------------------------------------------------------------------------
-- Execution output, in chunks numbered from the start of it.
--
-- A chunk is written once and read many times; it changes only when a capture
-- starts over or the front is dropped past the ceiling.
-- -----------------------------------------------------------------------------

-- name: DropExecutionLogs :exec
-- Deletes the chunks before keep_from, and every one from seq on, ahead of
-- writing chunk seq.
DELETE FROM execution_logs
WHERE execution_id = @execution_id AND (seq < @keep_from::bigint OR seq >= @seq::bigint);

-- name: InsertExecutionLog :exec
INSERT INTO execution_logs (execution_id, seq, data) VALUES (@execution_id, @seq, @data);

-- name: NextExecutionLog :one
-- The first chunk from seq on, so a read takes one chunk at a time.
SELECT seq, data FROM execution_logs
WHERE execution_id = @execution_id AND seq >= @seq
ORDER BY seq
LIMIT 1;

-- name: PruneExecutionLogs :one
-- Deletes the output of every execution that ended before before, counting the
-- executions. Driven from the chunks, which retention keeps few, rather than
-- from every execution ever recorded.
WITH pruned AS (
    DELETE FROM execution_logs
    WHERE execution_id IN (
        SELECT l.execution_id FROM execution_logs l
        JOIN executions e ON e.id = l.execution_id
        WHERE e.ended_at < @before
    )
    RETURNING execution_id
)
SELECT count(DISTINCT execution_id) FROM pruned;
