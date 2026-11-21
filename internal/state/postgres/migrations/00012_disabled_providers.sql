-- Providers turned off through the API.
--
-- A row means an operator disabled that provider with `vagabond provider
-- disable`, and enabling it deletes the row. disabled_by is the common name on
-- the operator's client certificate, or '' when the API does not require one.
-- A provider turned off in the configuration file has no row.
--
-- Outside a transaction, as 00004 is, so CockroachDB applies the schema change
-- on its own.

-- +goose NO TRANSACTION

-- +goose Up
CREATE TABLE disabled_providers (
    name        TEXT        PRIMARY KEY,
    disabled_at TIMESTAMPTZ NOT NULL,
    disabled_by TEXT        NOT NULL
);

-- +goose Down
DROP TABLE disabled_providers;
