-- +goose Up
-- Runs as app_owner (MIGRATIONS_DATABASE_URL). Extensions and roles come from
-- infra/db/bootstrap.sql, which needs a superuser.

-- Postgres grants EXECUTE to PUBLIC on every new function. Revoke that default for
-- everything app_owner creates, so a function is only callable once it is granted.
ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

-- +goose Down
ALTER DEFAULT PRIVILEGES GRANT EXECUTE ON FUNCTIONS TO PUBLIC;
