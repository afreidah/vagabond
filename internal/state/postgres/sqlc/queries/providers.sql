-- -----------------------------------------------------------------------------
-- Providers turned off through the API.
--
-- Disabling a provider that is already disabled changes nothing, so the row
-- keeps the time and name of the first disable.
-- -----------------------------------------------------------------------------

-- name: DisableProvider :exec
INSERT INTO disabled_providers (name, disabled_at, disabled_by)
VALUES (@name, @disabled_at, @disabled_by)
ON CONFLICT (name) DO NOTHING;

-- name: EnableProvider :exec
DELETE FROM disabled_providers
WHERE name = @name;

-- name: ListDisabledProviders :many
SELECT name, disabled_at, disabled_by
FROM disabled_providers;
