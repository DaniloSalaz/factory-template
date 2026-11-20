-- +goose Up
-- Identity and tenancy schema (docs/designs/auth-y-tenancy.md, sections 4 and 7).
-- Runs as app_owner (MIGRATIONS_DATABASE_URL). Extensions and roles come from
-- infra/db/bootstrap.sql, which needs a superuser.

-- Postgres grants EXECUTE to PUBLIC on every new function. Revoke that default for
-- everything app_owner creates, so a function is only callable once it is granted.
ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

-- Keeps updated_at honest without relying on every UPDATE remembering it.
-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

------------------------------------------------------------------------------------
-- Identity tables (section 4.1). No tenant_id and no RLS: only the identity module
-- queries them.
------------------------------------------------------------------------------------

CREATE TABLE users (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  email          extensions.citext NOT NULL UNIQUE,
  email_verified boolean NOT NULL DEFAULT false,
  display_name   text NOT NULL DEFAULT '',
  last_login_at  timestamptz,
  disabled_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Reserved system user for system jobs (section 7.6): fixed UUID, no identities, no
-- login. It is disabled and has an unroutable address (RFC 2606 .invalid), so every
-- function that requires an active, verified user rejects it without a special case.
INSERT INTO users (id, email, email_verified, display_name, disabled_at)
VALUES ('00000000-0000-7000-8000-000000000001', 'system@system.invalid', false, 'System', now());

CREATE TABLE user_identities (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  provider      text NOT NULL CHECK (provider <> ''),
  subject       text NOT NULL CHECK (subject <> ''),
  email_at_link extensions.citext NOT NULL,
  linked_at     timestamptz NOT NULL DEFAULT now(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, subject)
);
CREATE INDEX user_identities_user_id_idx ON user_identities (user_id);

CREATE TABLE tenants (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  slug       text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$'),
  name       text NOT NULL CHECK (name <> ''),
  status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER tenants_set_updated_at BEFORE UPDATE ON tenants
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE sessions (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  -- SHA-256 of the cookie token; the token itself is never stored.
  token_hash          bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
  user_id             uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  auth_time           timestamptz NOT NULL,
  mfa_auth_time       timestamptz,
  last_seen_at        timestamptz NOT NULL DEFAULT now(),
  idle_expires_at     timestamptz NOT NULL,
  absolute_expires_at timestamptz NOT NULL,
  ip_prefix           cidr,
  user_agent          text,
  revoked_at          timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE TRIGGER sessions_set_updated_at BEFORE UPDATE ON sessions
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

------------------------------------------------------------------------------------
-- Platform tables with tenant_id (section 4.2). RLS forced, standard isolation policy
-- and a declared scope. Foreign keys to tenants are RESTRICT until tenant deletion is
-- designed.
------------------------------------------------------------------------------------

CREATE TABLE memberships (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  -- RESTRICT: deleting a user removes memberships explicitly, publishing
  -- identity.member_removed for each one (section 5.8).
  user_id    uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  -- Validated against the app's role catalog in Go; the database never evaluates it.
  role       text NOT NULL CHECK (role <> ''),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, user_id)
);
-- Serves auth_memberships_for_user (/me); tenant-scoped lookups use the unique index.
CREATE INDEX memberships_user_id_idx ON memberships (user_id);
CREATE TRIGGER memberships_set_updated_at BEFORE UPDATE ON memberships
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE invitations (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  email       extensions.citext NOT NULL,
  role        text NOT NULL CHECK (role <> ''),
  -- SHA-256 of the one-time token sent by email.
  token_hash  bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
  -- Nullable so that deleting the inviter (GDPR, section 5.8) keeps the history.
  invited_by  uuid REFERENCES users (id) ON DELETE SET NULL,
  expires_at  timestamptz NOT NULL,
  accepted_at timestamptz,
  revoked_at  timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (accepted_at IS NULL OR revoked_at IS NULL)
);
-- At most one pending invitation per email and tenant (section 5.6.2).
CREATE UNIQUE INDEX invitations_pending_email_key ON invitations (tenant_id, email)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;
-- Revoking the pending invitations of an inviter (section 5.6.4).
CREATE INDEX invitations_tenant_inviter_idx ON invitations (tenant_id, invited_by);
CREATE TRIGGER invitations_set_updated_at BEFORE UPDATE ON invitations
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE audit_log (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  -- Nullable: events without a tenant (login). Under RLS those rows are visible to no
  -- tenant; only app_admin reads them.
  tenant_id     uuid REFERENCES tenants (id) ON DELETE RESTRICT,
  -- SET NULL works although the table is immutable for the application: referential
  -- actions run with the table owner's rights and are exempt from RLS.
  actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
  action        text NOT NULL CHECK (action <> ''),
  target_type   text,
  target_id     text,
  -- No personal data (section 9); always a JSON object.
  metadata      jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
  ip_prefix     cidr,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_tenant_created_idx ON audit_log (tenant_id, created_at DESC);

------------------------------------------------------------------------------------
-- Row-level security (section 7.3).
--
-- tenant_isolation (permissive, FOR ALL) confines every row to app.tenant_id. Without
-- the variable, current_setting fails (missing in a new connection, '' in a reused one)
-- and the query errors instead of returning data. Policies never use
-- current_setting(..., true) or NULLIF, which would turn that error into an empty
-- match or, worse, a NULL comparison.
--
-- own_scope is RESTRICTIVE: it is ANDed with tenant_isolation. A permissive one would
-- be ORed with it and open the rows of every tenant. With only USING, Postgres also
-- applies it as the WITH CHECK of INSERT and UPDATE.
------------------------------------------------------------------------------------

ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE memberships FORCE ROW LEVEL SECURITY;

-- Every membership belongs to the current tenant, for reads and writes.
CREATE POLICY tenant_isolation ON memberships AS PERMISSIVE FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

-- Users with scope 'own' (a law firm's client) see and touch only their own membership.
CREATE POLICY own_scope ON memberships AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR user_id = current_setting('app.user_id')::uuid);

ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;

-- Every invitation belongs to the current tenant, for reads and writes.
CREATE POLICY tenant_isolation ON invitations AS PERMISSIVE FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

-- Invitations reveal other people's emails: nothing for scope 'own'.
CREATE POLICY own_scope ON invitations AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant');

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;

-- Audit events of the current tenant only.
CREATE POLICY tenant_isolation ON audit_log AS PERMISSIVE FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

-- The tenant's audit trail is not for scope 'own'.
CREATE POLICY own_scope ON audit_log AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant');

------------------------------------------------------------------------------------
-- Privileges. Granted explicitly per table, never through ALTER DEFAULT PRIVILEGES:
-- a new table is unreachable until its migration says who may use it and how
-- (docs/runbooks/database.md). No sequences exist (UUIDv7 keys).
------------------------------------------------------------------------------------

-- app_runtime: identity tables without RLS.
GRANT SELECT, INSERT, UPDATE, DELETE ON users, user_identities, sessions TO app_runtime;
-- Tenants are created only by create_tenant_with_owner, so none exists without an
-- owner. No DELETE (tenant deletion is a future admin command) and no UPDATE of status
-- (suspension is app_admin's): renaming is all the application does.
GRANT SELECT, UPDATE (name, updated_at) ON tenants TO app_runtime;
-- app_runtime: tenant tables, filtered by RLS.
GRANT SELECT, INSERT, UPDATE, DELETE ON invitations TO app_runtime;
-- Memberships are created only by create_tenant_with_owner and accept_invitation, which
-- revalidate what RLS cannot (an owner for every tenant, a valid invitation).
GRANT SELECT, UPDATE, DELETE ON memberships TO app_runtime;
-- The audit log is immutable for the application: it reads through RLS and writes only
-- through record_audit().
GRANT SELECT ON audit_log TO app_runtime;

-- app_admin (BYPASSRLS): administration commands such as deleting a user. It may read
-- the audit log but, like the application, never rewrite it.
GRANT SELECT, INSERT, UPDATE, DELETE
  ON users, user_identities, tenants, sessions, memberships, invitations TO app_admin;
GRANT SELECT ON audit_log TO app_admin;

-- +goose Down
DROP TABLE audit_log;
DROP TABLE invitations;
DROP TABLE memberships;
DROP TABLE sessions;
DROP TABLE tenants;
DROP TABLE user_identities;
DROP TABLE users;
DROP FUNCTION set_updated_at();
ALTER DEFAULT PRIVILEGES GRANT EXECUTE ON FUNCTIONS TO PUBLIC;
