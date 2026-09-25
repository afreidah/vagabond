-- Namespaces on quota counters and reservations.
--
-- A counter belongs to a provider's total (namespace '') or to one namespace's
-- share of the provider. Existing rows are totals.
--
-- Outside a transaction: CockroachDB refuses a primary key change in the same
-- transaction as the column it adds.

-- +goose NO TRANSACTION

-- +goose Up
ALTER TABLE quota_usage ADD COLUMN namespace TEXT NOT NULL DEFAULT '';

ALTER TABLE quota_usage
    DROP CONSTRAINT quota_usage_pkey,
    ADD CONSTRAINT quota_usage_pkey PRIMARY KEY (namespace, provider, pool, period);

ALTER TABLE quota_reservations ADD COLUMN namespace TEXT NOT NULL DEFAULT '';

ALTER TABLE quota_reservations
    DROP CONSTRAINT quota_reservations_pkey,
    ADD CONSTRAINT quota_reservations_pkey PRIMARY KEY (execution_id, namespace, pool);

DROP INDEX quota_reservations_pool;

CREATE INDEX quota_reservations_pool ON quota_reservations (namespace, provider, pool, period);

-- +goose Down
DROP INDEX quota_reservations_pool;

CREATE INDEX quota_reservations_pool ON quota_reservations (provider, pool, period);

ALTER TABLE quota_reservations
    DROP CONSTRAINT quota_reservations_pkey,
    ADD CONSTRAINT quota_reservations_pkey PRIMARY KEY (execution_id, pool);

ALTER TABLE quota_reservations DROP COLUMN namespace;

ALTER TABLE quota_usage
    DROP CONSTRAINT quota_usage_pkey,
    ADD CONSTRAINT quota_usage_pkey PRIMARY KEY (provider, pool, period);

ALTER TABLE quota_usage DROP COLUMN namespace;
