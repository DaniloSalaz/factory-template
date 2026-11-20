-- +goose Up
-- Optional MFA, required per role from the app's catalog (design, 5.2; ADR 0002).
--
-- WorkOS reports nothing about MFA in a login (design, section 13), so the app verifies
-- the second factor itself after the login: it challenges the user's TOTP factor through
-- the WorkOS MFA API and, only when WorkOS answers valid: true, mark_session_mfa
-- replaces the session with a new one (new token) whose mfa_auth_time is now. A session
-- counts as MFA only through that column.
--
-- app_runtime gets no privilege on the new tables and still no UPDATE on
-- sessions.mfa_auth_time or token_hash: everything goes through the SECURITY DEFINER
-- functions below, which read the acting user from app.user_id and revalidate the
-- session they act on.

------------------------------------------------------------------------------------
-- mfa_factors: the user's factors at the provider, by its id. Only what the rules need:
-- whether it is verified, and the last code accepted with it, so the same code is never
-- accepted twice (WorkOS does not document replay protection across challenges).
-- last_code_hash is the SHA-256 of the 6-digit code: guessable, but a used code is
-- worthless once its 30 s step has passed.
------------------------------------------------------------------------------------
CREATE TABLE mfa_factors (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  -- CASCADE: deleting a user (app_admin, section 5.8) removes its factors; the trigger
  -- below audits each one.
  user_id            uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  provider           text NOT NULL CHECK (provider <> ''),
  provider_factor_id text NOT NULL CHECK (provider_factor_id <> ''),
  verified_at        timestamptz,
  last_code_hash     bytea CHECK (octet_length(last_code_hash) = 32),
  last_code_at       timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, provider_factor_id),
  CHECK ((last_code_hash IS NULL) = (last_code_at IS NULL))
);
CREATE INDEX mfa_factors_user_idx ON mfa_factors (user_id);
CREATE TRIGGER mfa_factors_set_updated_at BEFORE UPDATE ON mfa_factors
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

------------------------------------------------------------------------------------
-- mfa_challenges: our own record of each WorkOS challenge, bound to one factor and one
-- session. WorkOS TOTP challenges never expire, so the expiry (5 minutes) and the single
-- use are ours: consumed_at is set by the first verification attempt, right or wrong,
-- and completed_at when it marked the session.
------------------------------------------------------------------------------------
CREATE TABLE mfa_challenges (
  id                    uuid PRIMARY KEY DEFAULT uuidv7(),
  factor_id             uuid NOT NULL REFERENCES mfa_factors (id) ON DELETE CASCADE,
  session_id            uuid NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
  provider_challenge_id text NOT NULL UNIQUE CHECK (provider_challenge_id <> ''),
  expires_at            timestamptz NOT NULL,
  consumed_at           timestamptz,
  completed_at          timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  CHECK (completed_at IS NULL OR consumed_at IS NOT NULL)
);
CREATE INDEX mfa_challenges_factor_idx ON mfa_challenges (factor_id);
CREATE INDEX mfa_challenges_session_idx ON mfa_challenges (session_id);

-- What the functions need, and nothing more (design, 7.4).
GRANT SELECT, INSERT ON public.sessions TO app_security;
GRANT UPDATE (revoked_at, updated_at) ON public.sessions TO app_security;
GRANT SELECT, INSERT, DELETE ON public.mfa_factors TO app_security;
GRANT UPDATE (verified_at, last_code_hash, last_code_at, updated_at) ON public.mfa_factors TO app_security;
GRANT SELECT, INSERT ON public.mfa_challenges TO app_security;
GRANT UPDATE (consumed_at, completed_at) ON public.mfa_challenges TO app_security;

-- Recovery of a user who lost their device (docs/runbooks/auth.md) until the admin
-- command of step 7: app_admin removes the factor, and the trigger audits it.
GRANT SELECT, DELETE ON public.mfa_factors TO app_admin;
GRANT SELECT ON public.mfa_challenges TO app_admin;

------------------------------------------------------------------------------------
-- mfa_factors_for_user: the acting user's own factors (GET /me and the MFA use cases).
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION mfa_factors_for_user()
RETURNS TABLE (id uuid, provider text, provider_factor_id text, verified_at timestamptz, created_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
  SELECT f.id, f.provider, f.provider_factor_id, f.verified_at, f.created_at
  FROM public.mfa_factors f
  WHERE f.user_id = current_setting('app.user_id')::uuid
  ORDER BY f.created_at, f.id;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION mfa_factors_for_user() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mfa_factors_for_user() TO app_runtime;
ALTER FUNCTION mfa_factors_for_user() OWNER TO app_security;

------------------------------------------------------------------------------------
-- register_mfa_factor: POST /me/mfa/totp. Records a factor just enrolled at the
-- provider, unverified, with the challenge the enrollment returned, bound to the
-- session of p_token_hash. At most 3 unverified factors per user (TN045), so an
-- abandoned enrollment cannot pile up. Whether the user may enroll is decided in Go.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION register_mfa_factor(p_token_hash bytea, p_provider text, p_provider_factor_id text,
                                    p_provider_challenge_id text)
RETURNS TABLE (factor_id uuid, challenge_id uuid, expires_at timestamptz)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
#variable_conflict use_column
DECLARE
  v_user    uuid := current_setting('app.user_id')::uuid;
  v_session uuid;
  v_factor  uuid;
  v_chal    uuid;
  v_expires timestamptz := now() + interval '5 minutes';
BEGIN
  SELECT s.id INTO v_session
  FROM public.sessions s JOIN public.users u ON u.id = s.user_id
  WHERE s.token_hash = p_token_hash AND s.user_id = v_user AND s.revoked_at IS NULL
    AND s.idle_expires_at > now() AND s.absolute_expires_at > now() AND u.disabled_at IS NULL;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no valid session of the acting user' USING ERRCODE = 'TN050';
  END IF;
  IF (SELECT count(*) FROM public.mfa_factors f WHERE f.user_id = v_user AND f.verified_at IS NULL) >= 3 THEN
    RAISE EXCEPTION 'too many unverified factors' USING ERRCODE = 'TN045';
  END IF;

  INSERT INTO public.mfa_factors (user_id, provider, provider_factor_id)
  VALUES (v_user, p_provider, p_provider_factor_id)
  RETURNING id INTO v_factor;
  INSERT INTO public.mfa_challenges (factor_id, session_id, provider_challenge_id, expires_at)
  VALUES (v_factor, v_session, p_provider_challenge_id, v_expires)
  RETURNING id INTO v_chal;
  RETURN QUERY SELECT v_factor, v_chal, v_expires;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION register_mfa_factor(bytea, text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION register_mfa_factor(bytea, text, text, text) TO app_runtime;
ALTER FUNCTION register_mfa_factor(bytea, text, text, text) OWNER TO app_security;

------------------------------------------------------------------------------------
-- create_mfa_challenge: POST /me/mfa/challenges. Records a provider challenge of one of
-- the acting user's factors, bound to the session of p_token_hash, valid 5 minutes.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION create_mfa_challenge(p_token_hash bytea, p_factor_id uuid, p_provider_challenge_id text)
RETURNS TABLE (challenge_id uuid, expires_at timestamptz)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
#variable_conflict use_column
DECLARE
  v_user    uuid := current_setting('app.user_id')::uuid;
  v_session uuid;
  v_chal    uuid;
  v_expires timestamptz := now() + interval '5 minutes';
BEGIN
  SELECT s.id INTO v_session
  FROM public.sessions s JOIN public.users u ON u.id = s.user_id
  WHERE s.token_hash = p_token_hash AND s.user_id = v_user AND s.revoked_at IS NULL
    AND s.idle_expires_at > now() AND s.absolute_expires_at > now() AND u.disabled_at IS NULL;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no valid session of the acting user' USING ERRCODE = 'TN050';
  END IF;
  PERFORM 1 FROM public.mfa_factors f WHERE f.id = p_factor_id AND f.user_id = v_user;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'factor not found' USING ERRCODE = 'TN040';
  END IF;

  INSERT INTO public.mfa_challenges (factor_id, session_id, provider_challenge_id, expires_at)
  VALUES (p_factor_id, v_session, p_provider_challenge_id, v_expires)
  RETURNING id INTO v_chal;
  RETURN QUERY SELECT v_chal, v_expires;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION create_mfa_challenge(bytea, uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION create_mfa_challenge(bytea, uuid, text) TO app_runtime;
ALTER FUNCTION create_mfa_challenge(bytea, uuid, text) OWNER TO app_security;

------------------------------------------------------------------------------------
-- consume_mfa_challenge: the first step of a verification. Locks the challenge, checks
-- it belongs to the acting user and to the session of p_token_hash, is unused and has
-- not expired, and marks it used whatever the code turns out to be: a challenge admits
-- one attempt. Commits before the code is sent to the provider.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION consume_mfa_challenge(p_token_hash bytea, p_challenge_id uuid)
RETURNS TABLE (provider_challenge_id text, factor_id uuid, factor_verified boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
#variable_conflict use_column
DECLARE
  v_user uuid := current_setting('app.user_id')::uuid;
  v_c    record;
BEGIN
  SELECT c.id, c.provider_challenge_id, c.factor_id, c.expires_at, c.consumed_at,
         f.verified_at IS NOT NULL AS verified
  INTO v_c
  FROM public.mfa_challenges c
  JOIN public.mfa_factors f ON f.id = c.factor_id
  JOIN public.sessions s ON s.id = c.session_id
  JOIN public.users u ON u.id = s.user_id
  WHERE c.id = p_challenge_id AND f.user_id = v_user AND s.user_id = v_user
    AND s.token_hash = p_token_hash AND s.revoked_at IS NULL
    AND s.idle_expires_at > now() AND s.absolute_expires_at > now() AND u.disabled_at IS NULL
  FOR UPDATE OF c;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'challenge not found' USING ERRCODE = 'TN041';
  END IF;
  IF v_c.consumed_at IS NOT NULL THEN
    RAISE EXCEPTION 'challenge already used' USING ERRCODE = 'TN042';
  END IF;
  IF v_c.expires_at <= now() THEN
    RAISE EXCEPTION 'challenge expired' USING ERRCODE = 'TN043';
  END IF;

  UPDATE public.mfa_challenges SET consumed_at = now() WHERE id = v_c.id;
  RETURN QUERY SELECT v_c.provider_challenge_id, v_c.factor_id, v_c.verified;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION consume_mfa_challenge(bytea, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION consume_mfa_challenge(bytea, uuid) TO app_runtime;
ALTER FUNCTION consume_mfa_challenge(bytea, uuid) OWNER TO app_security;

------------------------------------------------------------------------------------
-- mark_session_mfa: the second step, called only when the provider answered valid: true
-- for the code (decided in Go). Revalidates everything itself:
--   - the challenge was consumed by this very session in the last 5 minutes and has
--     not marked a session yet (TN041, TN042);
--   - the code was not the last one accepted with the factor in the last 10 minutes
--     (TN044); the factor row is locked, so two requests with the same code serialize
--     and the second one is refused;
--   - the session is the acting user's, live, and its user active (TN050).
-- Then it verifies the factor if it was not, and replaces the session: a new row with a
-- new token (p_new_token_hash) and mfa_auth_time = now(), the rest copied, and the old
-- one revoked, so a cookie stolen before the verification is worthless after it.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION mark_session_mfa(p_token_hash bytea, p_challenge_id uuid, p_code_hash bytea,
                                 p_new_token_hash bytea)
RETURNS TABLE (session_id uuid, mfa_auth_time timestamptz, absolute_expires_at timestamptz,
               factor_id uuid, first_verification boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
#variable_conflict use_column
DECLARE
  v_user   uuid := current_setting('app.user_id')::uuid;
  v_c      record;
  v_f      record;
  v_s      record;
  v_new    uuid;
  v_now    timestamptz := now();
BEGIN
  SELECT c.id, c.factor_id, c.session_id, c.consumed_at, c.completed_at INTO v_c
  FROM public.mfa_challenges c
  WHERE c.id = p_challenge_id
  FOR UPDATE;
  IF NOT FOUND OR v_c.consumed_at IS NULL OR v_c.consumed_at <= v_now - interval '5 minutes' THEN
    RAISE EXCEPTION 'challenge not found' USING ERRCODE = 'TN041';
  END IF;
  IF v_c.completed_at IS NOT NULL THEN
    RAISE EXCEPTION 'challenge already used' USING ERRCODE = 'TN042';
  END IF;

  SELECT s.id, s.user_id, s.auth_time, s.idle_expires_at, s.absolute_expires_at, s.ip_prefix,
         s.user_agent, s.provider_session_id
  INTO v_s
  FROM public.sessions s JOIN public.users u ON u.id = s.user_id
  WHERE s.id = v_c.session_id AND s.token_hash = p_token_hash AND s.user_id = v_user
    AND s.revoked_at IS NULL AND s.idle_expires_at > v_now AND s.absolute_expires_at > v_now
    AND u.disabled_at IS NULL
  FOR UPDATE OF s;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'no valid session of the acting user' USING ERRCODE = 'TN050';
  END IF;

  SELECT f.id, f.verified_at, f.last_code_hash, f.last_code_at INTO v_f
  FROM public.mfa_factors f
  WHERE f.id = v_c.factor_id AND f.user_id = v_user
  FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'factor not found' USING ERRCODE = 'TN040';
  END IF;
  IF v_f.last_code_hash = p_code_hash AND v_f.last_code_at > v_now - interval '10 minutes' THEN
    RAISE EXCEPTION 'code already used' USING ERRCODE = 'TN044';
  END IF;

  UPDATE public.mfa_factors
  SET verified_at = coalesce(verified_at, v_now), last_code_hash = p_code_hash, last_code_at = v_now
  WHERE id = v_f.id;
  UPDATE public.mfa_challenges SET completed_at = v_now WHERE id = v_c.id;

  INSERT INTO public.sessions (token_hash, user_id, auth_time, mfa_auth_time, last_seen_at,
                               idle_expires_at, absolute_expires_at, ip_prefix, user_agent,
                               provider_session_id)
  VALUES (p_new_token_hash, v_s.user_id, v_s.auth_time, v_now, v_now, v_s.idle_expires_at,
          v_s.absolute_expires_at, v_s.ip_prefix, v_s.user_agent, v_s.provider_session_id)
  RETURNING id INTO v_new;
  UPDATE public.sessions SET revoked_at = v_now WHERE id = v_s.id;

  RETURN QUERY SELECT v_new, v_now, v_s.absolute_expires_at, v_f.id, v_f.verified_at IS NULL;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION mark_session_mfa(bytea, uuid, bytea, bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mark_session_mfa(bytea, uuid, bytea, bytea) TO app_runtime;
ALTER FUNCTION mark_session_mfa(bytea, uuid, bytea, bytea) OWNER TO app_security;

------------------------------------------------------------------------------------
-- remove_mfa_factor: DELETE /me/mfa/totp/{factorId}. Deletes one of the acting user's
-- factors and returns its provider id, so Go removes it at the provider in the same
-- transaction. Whether the user may remove it is decided in Go. The trigger audits it.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION remove_mfa_factor(p_factor_id uuid)
RETURNS TABLE (provider text, provider_factor_id text)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
#variable_conflict use_column
DECLARE
  v_user     uuid := current_setting('app.user_id')::uuid;
  v_provider text;
  v_pfid     text;
BEGIN
  DELETE FROM public.mfa_factors f WHERE f.id = p_factor_id AND f.user_id = v_user
  RETURNING f.provider, f.provider_factor_id INTO v_provider, v_pfid;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'factor not found' USING ERRCODE = 'TN040';
  END IF;
  RETURN QUERY SELECT v_provider, v_pfid;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION remove_mfa_factor(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION remove_mfa_factor(uuid) TO app_runtime;
ALTER FUNCTION remove_mfa_factor(uuid) OWNER TO app_security;

------------------------------------------------------------------------------------
-- audit_mfa_factor_removed: every removed factor is audited, whoever removes it: the user
-- (remove_mfa_factor), app_admin recovering a lost device (runbook), or the cascade of a
-- deleted user. A trigger, because app_admin cannot write audit_log (issue #5). The
-- actor is app.user_id when set; metadata.by is the role that connected, and
-- metadata.reason the app.audit_reason the admin sets (docs/runbooks/auth.md). EXECUTE
-- goes to app_runtime only to follow the rule of every SECURITY DEFINER function:
-- PostgreSQL refuses to call a trigger function directly.
------------------------------------------------------------------------------------
-- +goose StatementBegin
CREATE FUNCTION audit_mfa_factor_removed()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, extensions, pg_temp
AS $$
BEGIN
  INSERT INTO public.audit_log (id, tenant_id, actor_user_id, action, target_type, target_id, metadata)
  VALUES (uuidv7(), NULL, nullif(current_setting('app.user_id', true), '')::uuid, 'mfa.factor_removed',
          'mfa_factor', OLD.id::text,
          jsonb_strip_nulls(jsonb_build_object(
            'user_id', OLD.user_id::text,
            'verified', OLD.verified_at IS NOT NULL,
            'by', session_user::text,
            'reason', nullif(current_setting('app.audit_reason', true), ''))));
  RETURN OLD;
END;
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION audit_mfa_factor_removed() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION audit_mfa_factor_removed() TO app_runtime;
-- CREATE TRIGGER needs EXECUTE on the function, which app_owner loses with ownership.
CREATE TRIGGER mfa_factors_audit_removed AFTER DELETE ON mfa_factors
  FOR EACH ROW EXECUTE FUNCTION audit_mfa_factor_removed();
ALTER FUNCTION audit_mfa_factor_removed() OWNER TO app_security;

-- +goose Down
-- The functions belong to app_security, so only it can drop them; app_owner may SET ROLE
-- to it (it is a member without INHERIT). The trigger and the tables are app_owner's.
DROP TRIGGER mfa_factors_audit_removed ON mfa_factors;
SET ROLE app_security;
DROP FUNCTION audit_mfa_factor_removed();
DROP FUNCTION remove_mfa_factor(uuid);
DROP FUNCTION mark_session_mfa(bytea, uuid, bytea, bytea);
DROP FUNCTION consume_mfa_challenge(bytea, uuid);
DROP FUNCTION create_mfa_challenge(bytea, uuid, text);
DROP FUNCTION register_mfa_factor(bytea, text, text, text);
DROP FUNCTION mfa_factors_for_user();
RESET ROLE;
REVOKE UPDATE (revoked_at, updated_at) ON public.sessions FROM app_security;
REVOKE SELECT, INSERT ON public.sessions FROM app_security;
DROP TABLE mfa_challenges;
DROP TABLE mfa_factors;
