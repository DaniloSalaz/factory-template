-- +goose Up
-- Invitations (design 5.6 and 5.7.5; plan step 5b).
--
-- Automatic revocation (5.6.4): a pending invitation is always backed by someone who
-- could still issue it. When its inviter is demoted to a role that no longer includes the
-- offered one (or lacks members:invite), removed or leaves, their pending invitations of
-- that tenant are revoked in the same transaction. The member functions of migration
-- 00005 are replaced to do it. Which roles the new role may still offer is a permission
-- question, so Go answers it (p_offerable_roles); the database only compares texts.
--
-- Lock order, the same everywhere so no two transactions wait on each other in a cycle:
-- the tenant row, then invitations, then memberships. The member functions lock the
-- tenant FOR UPDATE; creating, resending and revoking an invitation (Go, under RLS) and
-- accept_invitation take FOR KEY SHARE on it first. Without that, accept_invitation
-- would lock the invitation and then wait for the tenant (its membership's foreign key),
-- while a demotion holding the tenant waits for the same invitation: a deadlock.
--
-- Functions owned by app_security can only be dropped by it; app_owner reaches its
-- rights through SET ROLE (see 00005). New ones are created as app_owner, granted, and
-- then handed over, as in 00002.

GRANT UPDATE (revoked_at, updated_at) ON public.invitations TO app_security;

SET ROLE app_security;
DROP FUNCTION change_member_role(uuid, text, text, text, cidr);
DROP FUNCTION remove_member(uuid, text, text, cidr);
DROP FUNCTION leave_tenant(cidr);
DROP FUNCTION accept_invitation(bytea, cidr);
RESET ROLE;

------------------------------------------------------------------------------------
-- revoke_inviter_invitations: revokes the pending invitations that p_inviter issued in
-- p_tenant, except those offering one of p_keep_roles, and records invitation.revoked
-- for each one with p_reason. The actor is app.user_id. Expired but unrevoked ones count
-- as pending: resending would bring them back. A helper of the member functions, like
-- lock_tenant_members: SECURITY INVOKER and executable only by its owner.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION revoke_inviter_invitations(p_tenant uuid, p_inviter uuid, p_keep_roles text[],
                                           p_reason text, p_ip_prefix cidr)
RETURNS void
LANGUAGE sql VOLATILE SECURITY INVOKER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
  WITH revoked AS (
    UPDATE public.invitations i SET revoked_at = now()
    WHERE i.tenant_id = p_tenant AND i.invited_by = p_inviter
      AND i.accepted_at IS NULL AND i.revoked_at IS NULL
      AND NOT (i.role = ANY (coalesce(p_keep_roles, '{}')))
    RETURNING i.id, i.role
  )
  INSERT INTO public.audit_log (tenant_id, actor_user_id, action, target_type, target_id, metadata, ip_prefix)
  SELECT p_tenant, current_setting('app.user_id')::uuid, 'invitation.revoked', 'invitation', r.id::text,
         jsonb_build_object('reason', p_reason, 'role', r.role, 'invited_by', p_inviter), p_ip_prefix
  FROM revoked r;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION revoke_inviter_invitations(uuid, uuid, text[], text, cidr) FROM PUBLIC;
ALTER FUNCTION revoke_inviter_invitations(uuid, uuid, text[], text, cidr) OWNER TO app_security;

------------------------------------------------------------------------------------
-- change_member_role: as in 00005, and it revokes the member's pending invitations
-- whose role is not in p_offerable_roles: the roles the new role may offer, as Go
-- decided (empty if it lacks members:invite).
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION change_member_role(p_member uuid, p_role text, p_expected_actor_role text,
                                   p_expected_member_role text, p_offerable_roles text[],
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
  PERFORM public.revoke_inviter_invitations(v_tenant, p_member, p_offerable_roles, 'inviter_role_changed', p_ip_prefix);
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION change_member_role(uuid, text, text, text, text[], cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION change_member_role(uuid, text, text, text, text[], cidr) TO app_runtime;
ALTER FUNCTION change_member_role(uuid, text, text, text, text[], cidr) OWNER TO app_security;

------------------------------------------------------------------------------------
-- remove_member: as in 00005, and it revokes every pending invitation of the member.
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
  PERFORM public.revoke_inviter_invitations(v_tenant, p_member, NULL, 'inviter_removed', p_ip_prefix);
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION remove_member(uuid, text, text, cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION remove_member(uuid, text, text, cidr) TO app_runtime;
ALTER FUNCTION remove_member(uuid, text, text, cidr) OWNER TO app_security;

------------------------------------------------------------------------------------
-- leave_tenant: as in 00005, and it revokes every pending invitation of the user.
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
  PERFORM public.revoke_inviter_invitations(v_tenant, v_actor, NULL, 'inviter_left', p_ip_prefix);
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION leave_tenant(cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION leave_tenant(cidr) TO app_runtime;
ALTER FUNCTION leave_tenant(cidr) OWNER TO app_security;

------------------------------------------------------------------------------------
-- accept_invitation: POST /invitations/accept (5.6.5). The checks of 00002, in the same
-- order, with two changes: the tenant row is locked FOR KEY SHARE before the invitation
-- (see the lock order above), and membership.created names the user as its target, as
-- the events of 00005 do. Two accepts of the same token still serialize on the
-- invitation's row lock: the second re-reads it and finds it accepted (TN011).
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
  v_tenant        uuid;
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

  -- An invitation never moves to another tenant, so this unlocked read names the
  -- tenant to lock first; the row itself is read again under its lock.
  SELECT i.tenant_id INTO v_tenant FROM public.invitations i WHERE i.token_hash = p_token_hash;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'invitation not found' USING ERRCODE = 'TN010';
  END IF;
  SELECT t.status INTO v_tenant_status FROM public.tenants t WHERE t.id = v_tenant FOR KEY SHARE;

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
  VALUES (v_inv.tenant_id, v_user, 'invitation.accepted', 'invitation', v_inv.id::text,
          jsonb_build_object('role', v_inv.role), p_ip_prefix),
         (v_inv.tenant_id, v_user, 'membership.created', 'user', v_user::text,
          jsonb_build_object('role', v_inv.role, 'membership_id', v_member, 'invitation_id', v_inv.id), p_ip_prefix);

  RETURN QUERY SELECT v_member, v_inv.tenant_id, v_inv.role;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION accept_invitation(bytea, cidr) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION accept_invitation(bytea, cidr) TO app_runtime;
ALTER FUNCTION accept_invitation(bytea, cidr) OWNER TO app_security;

-- +goose Down
-- The 00005 member functions and the 00002 accept_invitation are not restored here:
-- their definitions are in those migrations. Going back means re-running them by hand.
SET ROLE app_security;
DROP FUNCTION accept_invitation(bytea, cidr);
DROP FUNCTION leave_tenant(cidr);
DROP FUNCTION remove_member(uuid, text, text, cidr);
DROP FUNCTION change_member_role(uuid, text, text, text, text[], cidr);
DROP FUNCTION revoke_inviter_invitations(uuid, uuid, text[], text, cidr);
RESET ROLE;
REVOKE UPDATE (revoked_at, updated_at) ON public.invitations FROM app_security;
