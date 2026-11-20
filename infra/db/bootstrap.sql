-- Database bootstrap: extensions schema and application roles.
--
-- Run once per environment by a superuser, connected to the application database.
-- Roles are cluster-wide and BYPASSRLS needs a superuser, so this cannot be a
-- migration. The script is idempotent: running it again re-applies attributes,
-- passwords, memberships and grants.
--
-- Passwords come from psql variables, never from this file:
--   psql -v ON_ERROR_STOP=1 \
--        -v app_owner_password=... -v app_runtime_password=... -v app_admin_password=... \
--        -f infra/db/bootstrap.sql
-- infra/db/initdb.sh does this inside the Postgres container.

\set ON_ERROR_STOP on

-- Extensions live in their own schema so that application schemas only contain
-- application objects and SECURITY DEFINER functions can pin a known search_path.
CREATE SCHEMA IF NOT EXISTS extensions;
CREATE EXTENSION IF NOT EXISTS citext SCHEMA extensions;
CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA extensions;
CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA extensions;

-- Roles. Created only if missing; attributes are (re)applied below every time.
SELECT format('CREATE ROLE %I', r)
FROM unnest(ARRAY['app_owner', 'app_runtime', 'app_security', 'app_admin']) AS r
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r)
\gexec

-- app_owner: owns the schema objects and runs migrations. No BYPASSRLS: as table
-- owner it is still subject to RLS because every RLS table uses FORCE.
ALTER ROLE app_owner WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS
  PASSWORD :'app_owner_password';

-- app_runtime: the application. Never BYPASSRLS and never the owner of anything.
ALTER ROLE app_runtime WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS
  PASSWORD :'app_runtime_password';

-- app_security: owner of the SECURITY DEFINER functions. NOLOGIN, so it can only
-- act through those functions; BYPASSRLS because they work across tenants.
ALTER ROLE app_security WITH NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION BYPASSRLS
  PASSWORD NULL;

-- app_admin: administration commands only (disable or delete a user), audited.
ALTER ROLE app_admin WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION BYPASSRLS
  PASSWORD :'app_admin_password';

-- Migrations create the functions as app_owner and then hand them to app_security.
-- ALTER ... OWNER requires being able to SET ROLE to the new owner; INHERIT FALSE
-- keeps app_owner from silently gaining app_security's privileges.
GRANT app_security TO app_owner WITH INHERIT FALSE, SET TRUE;

-- Database access: only the application roles may connect.
SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', current_database()) \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO app_owner, app_runtime, app_admin', current_database()) \gexec

-- Schemas. Nothing is granted to PUBLIC; each role gets exactly what it needs.
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO app_owner;
-- CREATE is required for app_security to become the owner of functions in public.
GRANT USAGE, CREATE ON SCHEMA public TO app_security;
GRANT USAGE ON SCHEMA public TO app_runtime, app_admin;

REVOKE ALL ON SCHEMA extensions FROM PUBLIC;
GRANT USAGE ON SCHEMA extensions TO app_owner, app_runtime, app_security, app_admin;

-- With citext in its own schema, `email = $1` would silently fall back to a
-- case-sensitive text comparison unless the schema is on the search_path.
SELECT format('ALTER ROLE %I IN DATABASE %I SET search_path = public, extensions', r, current_database())
FROM unnest(ARRAY['app_owner', 'app_runtime', 'app_admin']) AS r
\gexec
