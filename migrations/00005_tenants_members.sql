-- +goose Up
-- Tenants and member management (design, 5.5 and 5.7; plan step 5a; ADR 0003).
--
-- Invariant: every active tenant has at least one owner. The database guarantees it,
-- not only Go: a role change, a removal and leaving the tenant go through the SECURITY
-- DEFINER functions below, and app_runtime loses the direct UPDATE (role) and DELETE it
-- had on memberships. Each function locks the tenant row (SELECT ... FOR UPDATE)
-- before counting owners, so two of them in the same tenant run one after the other
-- and the second one counts what the first one left.
--
-- Go still decides who may do what (design, principle 1): the hierarchy of roles is
-- evaluated in the use case. The functions only compare the roles Go based its decision
-- on (p_expected_*) with the ones they find after taking the lock, and refuse if they
-- changed (TN062): a decision made on stale roles is never applied. The one role name
-- the database knows is 'owner', which the platform reserves (authz.RoleOwner).
--
-- New error codes (class TN; Go maps them to apperr in platform/db):
--   TN021 the acting user has reached the limit of tenants they may create
--   TN060 the user is not a member of the current tenant
--   TN061 the change would leave the tenant without an owner
--   TN062 the actor's or the member's role changed since the caller read it

------------------------------------------------------------------------------------
-- tenants: who created each one, for the per-user limit of create_tenant. SET NULL so
-- that deleting the user (app_admin, section 5.8) keeps the tenant. Tenants created
-- before this migration have none and count for nobody.
------------------------------------------------------------------------------------
ALTER TABLE tenants ADD COLUMN created_by uuid REFERENCES users (id) ON DELETE SET NULL;
CREATE INDEX tenants_created_by_idx ON tenants (created_by);

-- Slugs are 3 to 40 characters from now on (Go validates the same, plus the reserved
-- names). The template has no tenant with a longer slug; a deployment that has one must
-- rename it before this migration.
ALTER TABLE tenants DROP CONSTRAINT tenants_slug_check;
ALTER TABLE tenants ADD CONSTRAINT tenants_slug_format
  CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$');

------------------------------------------------------------------------------------
-- Privileges.
------------------------------------------------------------------------------------

-- Role changes and removals only through the functions below, which keep an owner in
-- every tenant (design, 7.1). Leaving the tenant (scope own included) is leave_tenant.
REVOKE UPDATE (role, updated_at) ON memberships FROM app_runtime;
REVOKE DELETE ON memberships FROM app_runtime;

-- What the functions need. UPDATE (updated_at) on tenants is only there for
-- SELECT ... FOR UPDATE, which requires UPDATE on some column; no function updates a
-- tenant.
GRANT UPDATE (updated_at) ON public.tenants TO app_security;
GRANT UPDATE (role, updated_at) ON public.memberships TO app_security;
GRANT DELETE ON public.memberships TO app_security;

-- Superseded by create_tenant, which adds the per-user limit and records the creator.
-- Nothing called it yet (POST /tenants arrives with this migration). Only its owner
-- may drop it, and app_owner holds app_security's rights only through SET ROLE.
SET ROLE app_security;
DROP FUNCTION create_tenant_with_owner(text, text, cidr);
RESET ROLE;

------------------------------------------------------------------------------------
-- create_tenant: POST /tenants (section 5.5). Creates the tenant and makes the acting
-- user its owner in one step, so no tenant exists without an owner. Revalidates that the
-- user exists and is not disabled, and that they have created fewer than p_max_tenants
-- tenants (the limit is configuration, so Go passes it). A transaction-level advisory
-- lock per user serializes their concurrent creations, so two at once cannot both pass
-- the count. Records tenant.created and membership.created: without app.tenant_id,
-- record_audit would refuse them (TN031).
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION create_tenant(p_name text, p_slug text, p_max_tenants integer, p_ip_prefix cidr DEFAULT NULL)
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
  IF p_max_tenants IS NULL OR p_max_tenants < 1 THEN
    RAISE EXCEPTION 'p_max_tenants must be at least 1' USING ERRCODE = 'invalid_parameter_value';
  END IF;

  SELECT u.disabled_at INTO v_disabled FROM public.users u WHERE u.id = v_user;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'acting user does not exist' USING ERRCODE = 'TN001';
  END IF;
  IF v_disabled IS NOT NULL THEN
    RAISE EXCEPTION 'acting user is disabled' USING ERRCODE = 'TN002';
  END IF;

  PERFORM pg_advisory_xact_lock(hashtextextended('create_tenant:' || v_user::text, 0));
  IF (SELECT count(*) FROM public.tenants t WHERE t.created_by = v_user) >= p_max_tenants THEN
    RAISE EXCEPTION 'tenant limit reached' USING ERRCODE = 'TN021';
  END IF;

  INSERT INTO public.tenants (name, slug, created_by) VALUES (p_name, p_slug, v_user)
  ON CONFLICT (slug) DO NOTHING
  RETURNING id INTO v_tenant;
  IF v_tenant IS NULL THEN
    RAISE EXCEPTION 'tenant slug already taken' USING ERRCODE = 'TN020';
  END IF;

  INSERT INTO public.memberships (tenant_id, user_id, role)
  VALUES (v_tenant, v_user, 'owner')
  RETURNING id INTO v_member;

  INSERT INTO public.audit_log (tenant_id, actor_user_id, action, target_type, target_id, metadata, ip_prefix)
  VALUES (v_tenant, v_user, 'tenant.created', 'tenant', v_tenant::text,
          jsonb_build_object('slug', p_slug), p_ip_prefix),
         (v_tenant, v_user, 'membership.created', 'user', v_user::text,
          jsonb_build_object('role', 'owner', 'membership_id', v_member), p_ip_prefix);

  RETURN QUERY SELECT v_tenant, v_member;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION create_tenant(text, text, integer, cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION create_tenant(text, text, integer, cidr) TO app_runtime;
ALTER FUNCTION create_tenant(text, text, integer, cidr) OWNER TO app_security;

------------------------------------------------------------------------------------
-- lock_tenant_members: the common start of the three functions that change who is
-- owner. Locks the current tenant's row, so they serialize per tenant, and refuses a
-- tenant that is not active. The tenant is always app.tenant_id, never a parameter:
-- they act only on the tenant of the transaction (WithTenantTx). SECURITY INVOKER and
-- without any EXECUTE grant: only its owner, app_security, may call it, which is what
-- the functions below run as. app_runtime cannot.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION lock_tenant_members()
RETURNS uuid
LANGUAGE plpgsql VOLATILE SECURITY INVOKER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
DECLARE
  v_tenant uuid := current_setting('app.tenant_id')::uuid;
  v_status text;
BEGIN
  SELECT t.status INTO v_status FROM public.tenants t WHERE t.id = v_tenant FOR UPDATE;
  IF v_status IS DISTINCT FROM 'active' THEN
    RAISE EXCEPTION 'tenant is not active' USING ERRCODE = 'TN014';
  END IF;
  RETURN v_tenant;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION lock_tenant_members() FROM PUBLIC;
ALTER FUNCTION lock_tenant_members() OWNER TO app_security;

------------------------------------------------------------------------------------
-- change_member_role: PATCH /tenant/members/{userId} (section 5.7). After the lock:
-- the member must exist in this tenant (TN060), demoting the last owner is refused
-- (TN061), and the actor's and the member's roles must still be the ones Go judged
-- (TN062). Records membership.role_changed with both roles. Setting the role a member
-- already has changes and records nothing.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION change_member_role(p_member uuid, p_role text, p_expected_actor_role text,
                                   p_expected_member_role text, p_ip_prefix cidr DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
DECLARE
  v_actor      uuid := current_setting('app.user_id')::uuid;
  v_tenant     uuid := public.lock_tenant_members();
  v_membership uuid;
  v_role       text;
  v_actor_role text;
BEGIN
  SELECT m.id, m.role INTO v_membership, v_role FROM public.memberships m
  WHERE m.tenant_id = v_tenant AND m.user_id = p_member FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'not a member of the tenant' USING ERRCODE = 'TN060';
  END IF;
  IF v_role = 'owner' AND p_role <> 'owner' AND NOT EXISTS (
       SELECT 1 FROM public.memberships m
       WHERE m.tenant_id = v_tenant AND m.role = 'owner' AND m.user_id <> p_member) THEN
    RAISE EXCEPTION 'the tenant would be left without an owner' USING ERRCODE = 'TN061';
  END IF;
  SELECT m.role INTO v_actor_role FROM public.memberships m
  WHERE m.tenant_id = v_tenant AND m.user_id = v_actor;
  IF v_actor_role IS DISTINCT FROM p_expected_actor_role OR v_role IS DISTINCT FROM p_expected_member_role THEN
    RAISE EXCEPTION 'a role changed concurrently' USING ERRCODE = 'TN062';
  END IF;
  IF v_role = p_role THEN
    RETURN;
  END IF;

  UPDATE public.memberships m SET role = p_role WHERE m.id = v_membership;
  INSERT INTO public.audit_log (tenant_id, actor_user_id, action, target_type, target_id, metadata, ip_prefix)
  VALUES (v_tenant, v_actor, 'membership.role_changed', 'user', p_member::text,
          jsonb_build_object('from_role', v_role, 'to_role', p_role, 'membership_id', v_membership), p_ip_prefix);
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION change_member_role(uuid, text, text, text, cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION change_member_role(uuid, text, text, text, cidr) TO app_runtime;
ALTER FUNCTION change_member_role(uuid, text, text, text, cidr) OWNER TO app_security;

------------------------------------------------------------------------------------
-- remove_member: DELETE /tenant/members/{userId} (section 5.7). The same checks as
-- change_member_role; removing the last owner is refused (TN061). Records
-- membership.removed with the member's role.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION remove_member(p_member uuid, p_expected_actor_role text, p_expected_member_role text,
                              p_ip_prefix cidr DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
DECLARE
  v_actor      uuid := current_setting('app.user_id')::uuid;
  v_tenant     uuid := public.lock_tenant_members();
  v_membership uuid;
  v_role       text;
  v_actor_role text;
BEGIN
  SELECT m.id, m.role INTO v_membership, v_role FROM public.memberships m
  WHERE m.tenant_id = v_tenant AND m.user_id = p_member FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'not a member of the tenant' USING ERRCODE = 'TN060';
  END IF;
  IF v_role = 'owner' AND NOT EXISTS (
       SELECT 1 FROM public.memberships m
       WHERE m.tenant_id = v_tenant AND m.role = 'owner' AND m.user_id <> p_member) THEN
    RAISE EXCEPTION 'the tenant would be left without an owner' USING ERRCODE = 'TN061';
  END IF;
  SELECT m.role INTO v_actor_role FROM public.memberships m
  WHERE m.tenant_id = v_tenant AND m.user_id = v_actor;
  IF v_actor_role IS DISTINCT FROM p_expected_actor_role OR v_role IS DISTINCT FROM p_expected_member_role THEN
    RAISE EXCEPTION 'a role changed concurrently' USING ERRCODE = 'TN062';
  END IF;

  DELETE FROM public.memberships m WHERE m.id = v_membership;
  INSERT INTO public.audit_log (tenant_id, actor_user_id, action, target_type, target_id, metadata, ip_prefix)
  VALUES (v_tenant, v_actor, 'membership.removed', 'user', p_member::text,
          jsonb_build_object('role', v_role, 'membership_id', v_membership), p_ip_prefix);
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION remove_member(uuid, text, text, cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION remove_member(uuid, text, text, cidr) TO app_runtime;
ALTER FUNCTION remove_member(uuid, text, text, cidr) OWNER TO app_security;

------------------------------------------------------------------------------------
-- leave_tenant: DELETE /tenant/members/me (section 5.7). The acting user leaves the
-- current tenant, whatever their role or scope; the last owner cannot (TN061). It does
-- not depend on the actor's role, so there is nothing to compare. Records
-- membership.left.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION leave_tenant(p_ip_prefix cidr DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
DECLARE
  v_actor      uuid := current_setting('app.user_id')::uuid;
  v_tenant     uuid := public.lock_tenant_members();
  v_membership uuid;
  v_role       text;
BEGIN
  SELECT m.id, m.role INTO v_membership, v_role FROM public.memberships m
  WHERE m.tenant_id = v_tenant AND m.user_id = v_actor FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'not a member of the tenant' USING ERRCODE = 'TN060';
  END IF;
  IF v_role = 'owner' AND NOT EXISTS (
       SELECT 1 FROM public.memberships m
       WHERE m.tenant_id = v_tenant AND m.role = 'owner' AND m.user_id <> v_actor) THEN
    RAISE EXCEPTION 'the tenant would be left without an owner' USING ERRCODE = 'TN061';
  END IF;

  DELETE FROM public.memberships m WHERE m.id = v_membership;
  INSERT INTO public.audit_log (tenant_id, actor_user_id, action, target_type, target_id, metadata, ip_prefix)
  VALUES (v_tenant, v_actor, 'membership.left', 'user', v_actor::text,
          jsonb_build_object('role', v_role, 'membership_id', v_membership), p_ip_prefix);
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION leave_tenant(cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION leave_tenant(cidr) TO app_runtime;
ALTER FUNCTION leave_tenant(cidr) OWNER TO app_security;

-- +goose Down
DROP FUNCTION leave_tenant(cidr);
DROP FUNCTION remove_member(uuid, text, text, cidr);
DROP FUNCTION change_member_role(uuid, text, text, text, cidr);
DROP FUNCTION lock_tenant_members();
DROP FUNCTION create_tenant(text, text, integer, cidr);
REVOKE DELETE ON public.memberships FROM app_security;
REVOKE UPDATE (role, updated_at) ON public.memberships FROM app_security;
REVOKE UPDATE (updated_at) ON public.tenants FROM app_security;
GRANT DELETE ON memberships TO app_runtime;
GRANT UPDATE (role, updated_at) ON memberships TO app_runtime;
ALTER TABLE tenants DROP CONSTRAINT tenants_slug_format;
ALTER TABLE tenants ADD CONSTRAINT tenants_slug_check
  CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$');
DROP INDEX tenants_created_by_idx;
ALTER TABLE tenants DROP COLUMN created_by;
-- create_tenant_with_owner is not restored: migration 00002 has its definition.
