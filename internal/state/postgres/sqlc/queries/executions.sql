-- -----------------------------------------------------------------------------
-- Execution records.
--
-- An update names the state the caller read the record in and changes nothing
-- if it has moved since, so two writers cannot silently overwrite each other.
-- -----------------------------------------------------------------------------

-- name: CreateExecution :exec
-- Columns in table order, so the parameters convert from the row type.
INSERT INTO executions (
    id, namespace, job, job_version, task, provider, attempt, previous_id,
    state, provider_id, failure, created_at, started_at, ended_at, updated_at,
    exit_code, duration_ms, billed_cpu, billed_memory, billed_ms, logs, logs_truncated,
    dispatch_id
) VALUES (
    @id, @namespace, @job, @job_version, @task, @provider, @attempt, @previous_id,
    @state, @provider_id, @failure, @created_at, @started_at, @ended_at, @updated_at,
    @exit_code, @duration_ms, @billed_cpu, @billed_memory, @billed_ms, @logs, @logs_truncated,
    @dispatch_id
);

-- name: UpdateExecution :execrows
-- Writes everything that changes after creation, only if the record is still
-- in from_state.
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
    logs           = @logs,
    logs_truncated = @logs_truncated
WHERE id = @id AND state = @from_state::text;

-- name: GetExecution :one
SELECT * FROM executions WHERE id = @id;

-- name: ListJobExecutions :many
-- A job's most recent executions, newest first.
SELECT * FROM executions
WHERE namespace = @namespace AND job = @job
ORDER BY created_at DESC
LIMIT @max_rows;
