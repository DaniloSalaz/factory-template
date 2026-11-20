-- +goose Up
-- SECURITY DEFINER primitives of the identity module (design, section 7.4).
--
-- Each function is granted before it is handed to app_security: once app_owner is no
-- longer the owner it cannot change the ACL (it is a member of app_security without
-- INHERIT), and the ownership change carries the existing grants over.
--
-- Common rules, checked by the schema test:
-- - owned by app_security (NOLOGIN, BYPASSRLS), so they work across tenants without
--   app_runtime ever bypassing RLS;
-- - SET search_path = pg_catalog, extensions, pg_temp, so no object created in a
--   writable schema can shadow what they call; application tables are always
--   schema-qualified (public.*);
-- - EXECUTE revoked from PUBLIC and granted only to app_runtime;
-- - the acting user is read from app.user_id, never from a parameter, and no parameter
--   is called user_id;
-- - each one revalidates everything it protects, and none evaluates permissions:
--   that is Go's job.
--
-- Error codes (class TN, implementation-defined; Go maps them to apperr):
--   TN001 acting user (app.user_id) does not exist
--   TN002 acting user is disabled
--   TN010 invitation not found
--   TN011 invitation already accepted
--   TN012 invitation revoked
--   TN013 invitation expired
--   TN014 tenant is not active
--   TN015 invitation email does not match the user's verified email
--   TN016 user is already a member of the tenant
--   TN020 tenant slug already taken
--   TN030 audit event tenant does not match app.tenant_id
--   TN031 audit event has a tenant but app.tenant_id is not set
-- A missing or empty app.user_id is a programming error and surfaces as Postgres' own
-- 42704 / 22P02 from current_setting and the uuid cast.
--
-- create_tenant_with_owner and accept_invitation run without app.tenant_id, so
-- record_audit would refuse their tenant's events (TN031): they write those events
-- themselves, in the same transaction. Their p_ip_prefix is the client IP already
-- truncated by the caller, never the full address; audit_log's CHECK refuses anything
-- but /24 (IPv4) or /48 (IPv6).

-- What the functions read and write, and nothing else. Column-level UPDATE on
-- invitations is the minimum for SELECT ... FOR UPDATE and for marking it accepted.
GRANT SELECT ON public.users, public.tenants, public.memberships, public.invitations TO app_security;
GRANT INSERT ON public.tenants, public.memberships, public.audit_log TO app_security;
GRANT UPDATE (accepted_at) ON public.invitations TO app_security;

------------------------------------------------------------------------------------
-- auth_membership: the session middleware's tenant check (section 5.4). Returns one
-- row only if the acting user is a member of the tenant; Go turns zero rows, a
-- suspended tenant or a disabled user into the same 403.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION auth_membership(p_tenant_id uuid)
RETURNS TABLE (role text, tenant_status text, user_disabled boolean)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
  SELECT m.role, t.status, u.disabled_at IS NOT NULL
  FROM public.memberships m
  JOIN public.tenants t ON t.id = m.tenant_id
  JOIN public.users u ON u.id = m.user_id
  WHERE m.tenant_id = p_tenant_id
    AND m.user_id = current_setting('app.user_id')::uuid;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION auth_membership(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_membership(uuid) TO app_runtime;
ALTER FUNCTION auth_membership(uuid) OWNER TO app_security;

------------------------------------------------------------------------------------
-- auth_memberships_for_user: the acting user's tenants and roles (GET /me). Only the
-- caller's own memberships, never anyone else's.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION auth_memberships_for_user()
RETURNS TABLE (tenant_id uuid, tenant_slug text, tenant_name text, tenant_status text, role text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
  SELECT t.id, t.slug, t.name, t.status, m.role
  FROM public.memberships m
  JOIN public.tenants t ON t.id = m.tenant_id
  WHERE m.user_id = current_setting('app.user_id')::uuid
  ORDER BY t.name, t.id;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION auth_memberships_for_user() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_memberships_for_user() TO app_runtime;
ALTER FUNCTION auth_memberships_for_user() OWNER TO app_security;

------------------------------------------------------------------------------------
-- create_tenant_with_owner: POST /tenants (section 5.5). Creates the tenant and makes
-- the acting user its owner in one step, so a tenant never exists without an owner and
-- app_runtime never inserts a membership outside RLS. Revalidates that the user exists
-- and is not disabled, and records tenant.created and membership.created.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION create_tenant_with_owner(p_name text, p_slug text, p_ip_prefix cidr DEFAULT NULL)
RETURNS TABLE (tenant_id uuid, membership_id uuid)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
#variable_conflict use_column
DECLARE
  v_user     uuid := current_setting('app.user_id')::uuid;
  v_disabled timestamptz;
  v_tenant   uuid;
  v_member   uuid;
BEGIN
  SELECT u.disabled_at INTO v_disabled FROM public.users u WHERE u.id = v_user;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'acting user does not exist' USING ERRCODE = 'TN001';
  END IF;
  IF v_disabled IS NOT NULL THEN
    RAISE EXCEPTION 'acting user is disabled' USING ERRCODE = 'TN002';
  END IF;

  INSERT INTO public.tenants (name, slug) VALUES (p_name, p_slug)
  ON CONFLICT (slug) DO NOTHING
  RETURNING id INTO v_tenant;
  IF v_tenant IS NULL THEN
    RAISE EXCEPTION 'tenant slug already taken' USING ERRCODE = 'TN020';
  END IF;

  INSERT INTO public.memberships (tenant_id, user_id, role)
  VALUES (v_tenant, v_user, 'owner')
  RETURNING id INTO v_member;

  INSERT INTO public.audit_log (tenant_id, actor_user_id, action, target_type, target_id, metadata, ip_prefix)
  VALUES (v_tenant, v_user, 'tenant.created', 'tenant', v_tenant::text, '{}', p_ip_prefix),
         (v_tenant, v_user, 'membership.created', 'membership', v_member::text,
          jsonb_build_object('role', 'owner'), p_ip_prefix);

  RETURN QUERY SELECT v_tenant, v_member;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION create_tenant_with_owner(text, text, cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION create_tenant_with_owner(text, text, cidr) TO app_runtime;
ALTER FUNCTION create_tenant_with_owner(text, text, cidr) OWNER TO app_security;

------------------------------------------------------------------------------------
-- accept_invitation: POST /invitations/accept (section 5.6.5), all in one call.
-- The invitation row is locked FOR UPDATE, so two concurrent accepts of the same token
-- serialize: the second one waits, re-reads the row and finds it accepted. Everything
-- is revalidated here, including the user's verified email read from users, never
-- passed in. Users and tenants are not locked: disabling the user or suspending the
-- tenant during an accept leaves a membership that grants nothing (a disabled user
-- cannot log in; a suspended tenant admits no one). Records invitation.accepted and
-- membership.created.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION accept_invitation(p_token_hash bytea, p_ip_prefix cidr DEFAULT NULL)
RETURNS TABLE (membership_id uuid, tenant_id uuid, role text)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
#variable_conflict use_column
DECLARE
  v_user          uuid := current_setting('app.user_id')::uuid;
  v_email         extensions.citext;
  v_verified      boolean;
  v_disabled      timestamptz;
  v_inv           public.invitations%ROWTYPE;
  v_tenant_status text;
  v_member        uuid;
BEGIN
  SELECT u.email, u.email_verified, u.disabled_at INTO v_email, v_verified, v_disabled
  FROM public.users u WHERE u.id = v_user;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'acting user does not exist' USING ERRCODE = 'TN001';
  END IF;
  IF v_disabled IS NOT NULL THEN
    RAISE EXCEPTION 'acting user is disabled' USING ERRCODE = 'TN002';
  END IF;

  SELECT * INTO v_inv FROM public.invitations i WHERE i.token_hash = p_token_hash FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'invitation not found' USING ERRCODE = 'TN010';
  END IF;
  IF v_inv.accepted_at IS NOT NULL THEN
    RAISE EXCEPTION 'invitation already accepted' USING ERRCODE = 'TN011';
  END IF;
  IF v_inv.revoked_at IS NOT NULL THEN
    RAISE EXCEPTION 'invitation revoked' USING ERRCODE = 'TN012';
  END IF;
  IF v_inv.expires_at <= now() THEN
    RAISE EXCEPTION 'invitation expired' USING ERRCODE = 'TN013';
  END IF;

  SELECT t.status INTO v_tenant_status FROM public.tenants t WHERE t.id = v_inv.tenant_id;
  IF v_tenant_status IS DISTINCT FROM 'active' THEN
    RAISE EXCEPTION 'tenant is not active' USING ERRCODE = 'TN014';
  END IF;

  -- citext comparison: case-insensitive, as the email column of both tables.
  IF NOT v_verified OR v_email <> v_inv.email THEN
    RAISE EXCEPTION 'invitation email does not match the verified email' USING ERRCODE = 'TN015';
  END IF;

  IF EXISTS (SELECT 1 FROM public.memberships m
             WHERE m.tenant_id = v_inv.tenant_id AND m.user_id = v_user) THEN
    RAISE EXCEPTION 'user is already a member' USING ERRCODE = 'TN016';
  END IF;

  BEGIN
    INSERT INTO public.memberships (tenant_id, user_id, role)
    VALUES (v_inv.tenant_id, v_user, v_inv.role)
    RETURNING id INTO v_member;
  EXCEPTION WHEN unique_violation THEN
    -- Another invitation to the same tenant was accepted concurrently.
    RAISE EXCEPTION 'user is already a member' USING ERRCODE = 'TN016';
  END;

  UPDATE public.invitations i SET accepted_at = now() WHERE i.id = v_inv.id;

  INSERT INTO public.audit_log (tenant_id, actor_user_id, action, target_type, target_id, metadata, ip_prefix)
  VALUES (v_inv.tenant_id, v_user, 'invitation.accepted', 'invitation', v_inv.id::text, '{}', p_ip_prefix),
         (v_inv.tenant_id, v_user, 'membership.created', 'membership', v_member::text,
          jsonb_build_object('role', v_inv.role), p_ip_prefix);

  RETURN QUERY SELECT v_member, v_inv.tenant_id, v_inv.role;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION accept_invitation(bytea, cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accept_invitation(bytea, cidr) TO app_runtime;
ALTER FUNCTION accept_invitation(bytea, cidr) OWNER TO app_security;

------------------------------------------------------------------------------------
-- tenant_status: system jobs check the tenant without a membership (section 7.6).
-- NULL if the tenant does not exist.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION tenant_status(p_tenant_id uuid)
RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
  SELECT t.status FROM public.tenants t WHERE t.id = p_tenant_id;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION tenant_status(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION tenant_status(uuid) TO app_runtime;
ALTER FUNCTION tenant_status(uuid) OWNER TO app_security;

------------------------------------------------------------------------------------
-- roles_in_use: the distinct role names stored in any tenant, so the app can warn about
-- roles missing from its catalog at start-up (section 8). Returns no tenant data.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION roles_in_use()
RETURNS SETOF text
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
  SELECT DISTINCT m.role FROM public.memberships m ORDER BY m.role;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION roles_in_use() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION roles_in_use() TO app_runtime;
ALTER FUNCTION roles_in_use() OWNER TO app_security;

------------------------------------------------------------------------------------
-- record_audit: the only way the application writes to audit_log, which it cannot
-- modify or delete. The actor is app.user_id, or NULL for anonymous events (login
-- failures): this is the one place allowed to read it with missing_ok. Inside a tenant
-- transaction the event belongs to that tenant or to none (TN030); without app.tenant_id
-- it belongs to none (TN031). Either way a bug cannot plant events in another tenant's
-- trail, nor can a request that names a tenant it has no access to. The id is generated
-- here so app_security needs no SELECT on audit_log.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION record_audit(
  p_tenant_id   uuid,
  p_action      text,
  p_target_type text DEFAULT NULL,
  p_target_id   text DEFAULT NULL,
  p_metadata    jsonb DEFAULT '{}',
  p_ip_prefix   cidr DEFAULT NULL
)
RETURNS uuid
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
DECLARE
  v_id     uuid := uuidv7();
  v_actor  uuid := nullif(current_setting('app.user_id', true), '')::uuid;
  v_tenant uuid := nullif(current_setting('app.tenant_id', true), '')::uuid;
BEGIN
  IF v_tenant IS NULL AND p_tenant_id IS NOT NULL THEN
    RAISE EXCEPTION 'audit event has a tenant but app.tenant_id is not set' USING ERRCODE = 'TN031';
  END IF;
  IF v_tenant IS NOT NULL AND p_tenant_id IS NOT NULL AND p_tenant_id <> v_tenant THEN
    RAISE EXCEPTION 'audit event tenant does not match app.tenant_id' USING ERRCODE = 'TN030';
  END IF;

  INSERT INTO public.audit_log
    (id, tenant_id, actor_user_id, action, target_type, target_id, metadata, ip_prefix)
  VALUES
    (v_id, p_tenant_id, v_actor, p_action, p_target_type, p_target_id, p_metadata, p_ip_prefix);
  RETURN v_id;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION record_audit(uuid, text, text, text, jsonb, cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION record_audit(uuid, text, text, text, jsonb, cidr) TO app_runtime;
ALTER FUNCTION record_audit(uuid, text, text, text, jsonb, cidr) OWNER TO app_security;

-- +goose Down
DROP FUNCTION record_audit(uuid, text, text, text, jsonb, cidr);
DROP FUNCTION roles_in_use();
DROP FUNCTION tenant_status(uuid);
DROP FUNCTION accept_invitation(bytea, cidr);
DROP FUNCTION create_tenant_with_owner(text, text, cidr);
DROP FUNCTION auth_memberships_for_user();
DROP FUNCTION auth_membership(uuid);
REVOKE UPDATE (accepted_at) ON public.invitations FROM app_security;
REVOKE INSERT ON public.tenants, public.memberships, public.audit_log FROM app_security;
REVOKE SELECT ON public.users, public.tenants, public.memberships, public.invitations FROM app_security;
