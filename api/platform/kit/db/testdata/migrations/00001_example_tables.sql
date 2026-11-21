-- +goose Up
-- Example business tables for the tests of platform/db only: never in migrations/, so
-- no environment has them and runtime_privileges.golden does not list them. They follow
-- the patterns of design section 7.3 that every app copies:
--   notes           a row with an owner (own_scope on owner_user_id);
--   cases           a row reachable through an access table (own_scope with EXISTS);
--   case_access     the access table itself ('scope: access': own_scope on user_id and
--                   writes for scope 'tenant' only);
--   case_documents  a child table, scoped through its parent's access rows.

CREATE TABLE notes (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  owner_user_id uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  body          text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notes_tenant_owner_idx ON notes (tenant_id, owner_user_id);
CREATE TRIGGER notes_set_updated_at BEFORE UPDATE ON notes
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE cases (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  title      text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- Target of the children's composite keys; also the tenant-first index.
  UNIQUE (tenant_id, id)
);
CREATE TRIGGER cases_set_updated_at BEFORE UPDATE ON cases
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The composite keys (tenant_id, case_id) make a child of another tenant's case
-- impossible, whatever the code passes.
CREATE TABLE case_access (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  case_id    uuid NOT NULL,
  user_id    uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, case_id) REFERENCES cases (tenant_id, id) ON DELETE CASCADE,
  UNIQUE (tenant_id, case_id, user_id)
);
-- Serves the EXISTS of the own_scope policies and the removal of a member's rows.
CREATE INDEX case_access_tenant_user_idx ON case_access (tenant_id, user_id, case_id);

CREATE TABLE case_documents (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  case_id    uuid NOT NULL,
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, case_id) REFERENCES cases (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX case_documents_tenant_case_idx ON case_documents (tenant_id, case_id);
CREATE TRIGGER case_documents_set_updated_at BEFORE UPDATE ON case_documents
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

------------------------------------------------------------------------------------
-- Row-level security: tenant_isolation everywhere, plus the declared scope.
------------------------------------------------------------------------------------

ALTER TABLE notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE notes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notes AS PERMISSIVE FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
-- Scope 'own' sees and writes only its own notes.
CREATE POLICY own_scope ON notes AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR owner_user_id = current_setting('app.user_id')::uuid);

ALTER TABLE cases ENABLE ROW LEVEL SECURITY;
ALTER TABLE cases FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON cases AS PERMISSIVE FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
-- Scope 'own' sees only the cases it has an access row for. The subquery runs under
-- case_access's own policies, which already confine it to the user's rows.
CREATE POLICY own_scope ON cases AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR EXISTS (SELECT 1 FROM case_access a
                    WHERE a.tenant_id = cases.tenant_id
                      AND a.case_id = cases.id
                      AND a.user_id = current_setting('app.user_id')::uuid));

ALTER TABLE case_access ENABLE ROW LEVEL SECURITY;
ALTER TABLE case_access FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON case_access AS PERMISSIVE FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
-- Scope 'own' sees only its own access rows.
CREATE POLICY own_scope ON case_access AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR user_id = current_setting('app.user_id')::uuid);
-- An access table: own_scope alone would let scope 'own' insert an access row for
-- itself, or move its row to another case, and so reach any case of the tenant.
-- Granting, changing and removing access is for scope 'tenant' only; the structure test
-- requires these three policies on every table marked 'scope: access'.
COMMENT ON TABLE case_access IS 'scope: access';
CREATE POLICY own_write_insert ON case_access AS RESTRICTIVE FOR INSERT
  WITH CHECK (current_setting('app.scope') = 'tenant');
CREATE POLICY own_write_update ON case_access AS RESTRICTIVE FOR UPDATE
  USING (current_setting('app.scope') = 'tenant');
CREATE POLICY own_write_delete ON case_access AS RESTRICTIVE FOR DELETE
  USING (current_setting('app.scope') = 'tenant');

ALTER TABLE case_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE case_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON case_documents AS PERMISSIVE FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
-- Through the parent: scope 'own' sees the documents of the cases it has access to.
CREATE POLICY own_scope ON case_documents AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR EXISTS (SELECT 1 FROM case_access a
                    WHERE a.tenant_id = case_documents.tenant_id
                      AND a.case_id = case_documents.case_id
                      AND a.user_id = current_setting('app.user_id')::uuid));

------------------------------------------------------------------------------------
-- Privileges: explicit per table, UPDATE per column. case_access has no UPDATE: an
-- access row is granted or removed, never moved to another user or case.
------------------------------------------------------------------------------------

GRANT SELECT, INSERT, DELETE ON notes, cases, case_access, case_documents TO app_runtime;
GRANT UPDATE (body, updated_at) ON notes TO app_runtime;
GRANT UPDATE (title, updated_at) ON cases TO app_runtime;
GRANT UPDATE (name, updated_at) ON case_documents TO app_runtime;

-- +goose Down
DROP TABLE case_documents;
DROP TABLE case_access;
DROP TABLE cases;
DROP TABLE notes;
