-- -----------------------------------------------------------------------------
-- Quota usage and reservations.
--
-- Every mutation is one statement, run in a serializable transaction by the
-- caller. Serializable is what makes Reserve exact on Postgres: under its
-- default isolation two reserves can both read room and both insert.
-- -----------------------------------------------------------------------------

-- name: ReserveQuota :execrows
-- Inserts one reservation row per pool, only if every pool the execution
-- charges has room: used + reserved + amount <= limit. Zero rows means refused.
-- The headroom test and the claim are one statement, so nothing lands between.
--
-- The per-pool arrays are zipped by subscript rather than multi-argument
-- unnest, which sqlc's catalog does not know.
WITH wanted AS (
    SELECT
        (@pools::text[])[g.i]     AS pool,
        (@periods::text[])[g.i]   AS period,
        (@amounts::bigint[])[g.i] AS amount,
        (@limits::bigint[])[g.i]  AS pool_limit
    FROM generate_series(1, array_length(@pools::text[], 1)) AS g (i)
),
standing AS (
    SELECT
        wanted.pool,
        wanted.period,
        wanted.amount,
        wanted.pool_limit,
        COALESCE((
            SELECT u.used FROM quota_usage u
            WHERE u.provider = @provider::text AND u.pool = wanted.pool AND u.period = wanted.period
        ), 0) + COALESCE((
            SELECT SUM(r.amount) FROM quota_reservations r
            WHERE r.provider = @provider::text AND r.pool = wanted.pool AND r.period = wanted.period
        ), 0) AS charged
    FROM wanted
)
INSERT INTO quota_reservations (execution_id, provider, pool, period, amount, cpu, memory, created_at)
SELECT @execution_id::text, @provider::text, s.pool, s.period, s.amount, @cpu::bigint, @memory::bigint, @created_at::timestamptz
FROM standing s
WHERE NOT EXISTS (
    SELECT 1 FROM standing x
    WHERE x.amount > 0 AND x.charged + x.amount > x.pool_limit
);

-- name: SettleQuota :exec
-- Deletes an execution's reservation rows and adds what it actually cost, in
-- the periods it was reserved in. An execution with no rows settles to nothing,
-- so settling twice, or after the reaper, charges once. No amounts drops the
-- reservation outright.
WITH done AS (
    DELETE FROM quota_reservations
    WHERE execution_id = @execution_id::text
    RETURNING provider, pool, period
),
actual AS (
    SELECT
        (@pools::text[])[g.i]     AS pool,
        (@amounts::bigint[])[g.i] AS amount
    FROM generate_series(1, array_length(@pools::text[], 1)) AS g (i)
)
INSERT INTO quota_usage (provider, pool, period, used, updated_at)
SELECT done.provider, done.pool, done.period, actual.amount, NOW()
FROM done
JOIN actual ON actual.pool = done.pool
WHERE actual.amount <> 0
ON CONFLICT (provider, pool, period) DO UPDATE SET
    used       = quota_usage.used + EXCLUDED.used,
    updated_at = NOW();

-- name: ReadQuotaUsage :many
-- Returns settled plus reserved per pool in the given periods, which is what
-- admission and ranking read as a provider's standing.
SELECT charged.provider, charged.pool, charged.period, SUM(charged.amount)::bigint AS used
FROM (
    SELECT u.provider, u.pool, u.period, u.used AS amount
    FROM quota_usage u
    WHERE u.period = ANY(@periods::text[])
    UNION ALL
    SELECT r.provider, r.pool, r.period, r.amount
    FROM quota_reservations r
    WHERE r.period = ANY(@periods::text[])
) AS charged
GROUP BY charged.provider, charged.pool, charged.period;

-- name: ClaimStaleReservations :many
-- Locks reservation rows older than the cutoff, skipping any another process
-- holds, so two reapers never resolve the same execution.
SELECT execution_id, provider, pool, period, amount, cpu, memory, created_at
FROM quota_reservations
WHERE created_at < @before::timestamptz
ORDER BY execution_id, pool
FOR UPDATE SKIP LOCKED;
