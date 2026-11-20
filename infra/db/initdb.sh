#!/usr/bin/env bash
# Runs infra/db/bootstrap.sql as the container superuser.
#
# Mounted in /docker-entrypoint-initdb.d, so the official image runs it once, when the
# data volume is empty. It is idempotent and can also be run later with
# `mise run db:bootstrap`. Passwords come from the container environment.
set -euo pipefail

: "${APP_OWNER_PASSWORD:?APP_OWNER_PASSWORD is required}"
: "${APP_RUNTIME_PASSWORD:?APP_RUNTIME_PASSWORD is required}"
: "${APP_ADMIN_PASSWORD:?APP_ADMIN_PASSWORD is required}"

psql -v ON_ERROR_STOP=1 \
  --username "${POSTGRES_USER}" --dbname "${POSTGRES_DB}" \
  -v app_owner_password="${APP_OWNER_PASSWORD}" \
  -v app_runtime_password="${APP_RUNTIME_PASSWORD}" \
  -v app_admin_password="${APP_ADMIN_PASSWORD}" \
  -f /bootstrap/bootstrap.sql
