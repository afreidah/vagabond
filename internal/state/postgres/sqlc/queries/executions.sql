-- -----------------------------------------------------------------------------
-- Execution records.
--
-- Every write is conditional on the dispatch's lease, owner and epoch, and
-- carries the whole record, so repeating one changes nothing.
-- -----------------------------------------------------------------------------

-- name: CreateExecution :execrows
INSERT INTO executions (
    id, namespace, job, job_version, task, provider, attempt, previous_id,
    state, provider_id, failure, created_at, started_at, ended_at, updated_at,
    exit_code, duration_ms, billed_cpu, billed_memory, billed_ms, logs_truncated,
    dispatch_id, cpu, memory, released_at, reason, logs_start, logs_at, timeout_ms, node
)
SELECT
    @id, @namespace, @job, @job_version, @task, @provider, @attempt, @previous_id,
    @state, @provider_id, @failure, @created_at, @started_at, @ended_at, @updated_at,
    @exit_code, @duration_ms, @billed_cpu, @billed_memory, @billed_ms, @logs_truncated,
    @dispatch_id::text, @cpu, @memory, @released_at, @reason, @logs_start, @logs_at, @timeout_ms, @node
WHERE EXISTS (
    SELECT 1 FROM dispatches
    WHERE dispatches.dispatch_id = @dispatch_id::text
      AND dispatches.owner = @owner::text AND dispatches.epoch = @epoch::bigint
      AND dispatches.done_at IS NULL
);

-- name: SaveExecution :execrows
-- Writes everything that changes after creation.
UPDATE executions SET
    state          = @state,
    provider_id    = @provider_id,
    failure        = @failure,
    started_at     = @started_at,
    ended_at       = @ended_at,
    updated_at     = @updated_at,
    exit_code      = @exit_code,
    duration_ms    = @duration_ms,
    billed_cpu     = @billed_cpu,
    billed_memory  = @billed_memory,
    billed_ms      = @billed_ms,
    logs_truncated = @logs_truncated,
    reason         = @reason,
    logs_start     = @logs_start,
    logs_at        = @logs_at,
    released_at    = @released_at
WHERE id = @id AND EXISTS (
    SELECT 1 FROM dispatches
    WHERE dispatches.dispatch_id = executions.dispatch_id
      AND dispatches.owner = @owner::text AND dispatches.epoch = @epoch::bigint
      AND dispatches.done_at IS NULL
);

-- name: HoldsExecution :one
-- Whether the caller holds the lease of the execution's dispatch.
SELECT EXISTS (
    SELECT 1 FROM executions
    JOIN dispatches ON dispatches.dispatch_id = executions.dispatch_id
    WHERE executions.id = @id
      AND dispatches.owner = @owner::text AND dispatches.epoch = @epoch::bigint
      AND dispatches.done_at IS NULL
);

-- name: GetExecution :one
SELECT * FROM executions WHERE id = @id;

-- name: ListDispatchExecutions :many
-- Every execution of one dispatch, in the order they were created.
SELECT * FROM executions
WHERE dispatch_id = @dispatch_id
ORDER BY created_at;

-- name: ListJobExecutions :many
-- A job's most recent executions, newest first.
SELECT * FROM executions
WHERE namespace = @namespace AND job = @job
ORDER BY created_at DESC
LIMIT @max_rows;
