-- Identity tables have no RLS (design, 4.1): only these queries touch them. citext
-- columns are cast to text so sqlc maps them to string.

-- name: UserByIdentity :one
SELECT u.id, u.email::text AS email, (u.disabled_at IS NOT NULL)::boolean AS disabled
FROM user_identities i
JOIN users u ON u.id = i.user_id
WHERE i.provider = $1 AND i.subject = $2;

-- name: UserByEmail :one
SELECT u.id, u.email::text AS email, (u.disabled_at IS NOT NULL)::boolean AS disabled
FROM users u
WHERE u.email = sqlc.arg(email)::text::extensions.citext;

-- name: CreateUser :one
INSERT INTO users (email, email_verified, display_name)
VALUES (sqlc.arg(email)::text::extensions.citext, sqlc.arg(email_verified), sqlc.arg(display_name))
RETURNING id;

-- name: CreateIdentity :exec
INSERT INTO user_identities (user_id, provider, subject, email_at_link)
VALUES (sqlc.arg(user_id), sqlc.arg(provider), sqlc.arg(subject), sqlc.arg(email)::text::extensions.citext);

-- A new email always comes with the provider's verification of it (issue #4).
-- name: UpdateUserEmail :exec
UPDATE users
SET email = sqlc.arg(email)::text::extensions.citext, email_verified = sqlc.arg(email_verified)
WHERE id = sqlc.arg(id);

-- name: SetLastLogin :exec
UPDATE users SET last_login_at = now() WHERE id = $1;

-- A new session has no MFA: only mark_session_mfa gives it one (design, 5.2).
-- name: CreateSession :one
INSERT INTO sessions (token_hash, user_id, auth_time, last_seen_at,
                      idle_expires_at, absolute_expires_at, ip_prefix, user_agent, provider_session_id)
VALUES (sqlc.arg(token_hash), sqlc.arg(user_id), sqlc.arg(auth_time),
        sqlc.arg(now), sqlc.arg(idle_expires_at), sqlc.arg(absolute_expires_at),
        sqlc.narg(ip_prefix), sqlc.narg(user_agent), sqlc.narg(provider_session_id))
RETURNING id;

-- name: SessionByHash :one
SELECT s.id, s.user_id, s.mfa_auth_time, s.last_seen_at, s.idle_expires_at,
       s.absolute_expires_at, s.revoked_at, s.provider_session_id,
       (u.disabled_at IS NOT NULL)::boolean AS user_disabled
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1;

-- The WHERE on last_seen_at makes concurrent touches of the same session a no-op.
-- name: TouchSession :exec
UPDATE sessions
SET last_seen_at = sqlc.arg(now), idle_expires_at = sqlc.arg(idle_expires_at)
WHERE id = sqlc.arg(id) AND last_seen_at < sqlc.arg(now) AND revoked_at IS NULL;

-- name: RevokeSessionByHash :exec
UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeUserSessions :execrows
UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- Events without a tenant: login and logout never have one (record_audit, TN031).
-- name: RecordAudit :exec
SELECT record_audit(NULL::uuid, sqlc.arg(action), sqlc.narg(target_type), sqlc.narg(target_id),
                    sqlc.arg(metadata), sqlc.narg(ip_prefix));

-- The distinct roles stored in any tenant, without tenant data (design, section 8).
-- name: RolesInUse :many
SELECT r.role::text AS role FROM roles_in_use() AS r(role);

-- The acting user's membership in a tenant (tenant middleware, design 5.4.2). No row:
-- no membership, or no such tenant.
-- name: AuthMembership :one
SELECT m.role::text AS role, m.tenant_status::text AS tenant_status,
       m.user_disabled::boolean AS user_disabled
FROM auth_membership(sqlc.arg(tenant_id)::uuid) AS m(role, tenant_status, user_disabled);

-- Events of the transaction's tenant; record_audit refuses any other (TN030).
-- name: RecordTenantAudit :exec
SELECT record_audit(sqlc.arg(tenant_id)::uuid, sqlc.arg(action), sqlc.narg(target_type), sqlc.narg(target_id),
                    sqlc.arg(metadata), sqlc.narg(ip_prefix));

-- name: UserByID :one
SELECT u.id, u.email::text AS email, u.display_name
FROM users u
WHERE u.id = $1;

-- The acting user's own memberships, never anyone else's (GET /me).
-- name: MembershipsForUser :many
SELECT m.tenant_id::uuid AS tenant_id, m.tenant_slug::text AS tenant_slug, m.tenant_name::text AS tenant_name,
       m.tenant_status::text AS tenant_status, m.role::text AS role
FROM auth_memberships_for_user() AS m(tenant_id, tenant_slug, tenant_name, tenant_status, role);

-- Under RLS, inside the tenant's own transaction; the WHERE is defence in depth.
-- name: TenantByID :one
SELECT t.id, t.slug, t.name FROM tenants t WHERE t.id = $1;

-- The user's subject at a provider (WorkOS: the user id its MFA API takes).
-- name: IdentitySubject :one
SELECT i.subject FROM user_identities i WHERE i.user_id = $1 AND i.provider = $2;

-- MFA (design 5.2, migration 00004): every write goes through these functions.

-- name: MFAFactorsForUser :many
SELECT f.id::uuid AS id, f.provider::text AS provider, f.provider_factor_id::text AS provider_factor_id,
       f.verified_at::timestamptz AS verified_at, f.created_at::timestamptz AS created_at
FROM mfa_factors_for_user() AS f(id, provider, provider_factor_id, verified_at, created_at);

-- name: RegisterMFAFactor :one
SELECT r.factor_id::uuid AS factor_id, r.challenge_id::uuid AS challenge_id, r.expires_at::timestamptz AS expires_at
FROM register_mfa_factor(sqlc.arg(token_hash)::bytea, sqlc.arg(provider)::text, sqlc.arg(provider_factor_id)::text,
                         sqlc.arg(provider_challenge_id)::text) AS r(factor_id, challenge_id, expires_at);

-- name: CreateMFAChallenge :one
SELECT r.challenge_id::uuid AS challenge_id, r.expires_at::timestamptz AS expires_at
FROM create_mfa_challenge(sqlc.arg(token_hash)::bytea, sqlc.arg(factor_id)::uuid,
                          sqlc.arg(provider_challenge_id)::text) AS r(challenge_id, expires_at);

-- name: ConsumeMFAChallenge :one
SELECT r.provider_challenge_id::text AS provider_challenge_id, r.factor_id::uuid AS factor_id,
       r.factor_verified::boolean AS factor_verified
FROM consume_mfa_challenge(sqlc.arg(token_hash)::bytea, sqlc.arg(challenge_id)::uuid)
  AS r(provider_challenge_id, factor_id, factor_verified);

-- name: MarkSessionMFA :one
SELECT r.session_id::uuid AS session_id, r.mfa_auth_time::timestamptz AS mfa_auth_time,
       r.absolute_expires_at::timestamptz AS absolute_expires_at, r.factor_id::uuid AS factor_id,
       r.first_verification::boolean AS first_verification
FROM mark_session_mfa(sqlc.arg(token_hash)::bytea, sqlc.arg(challenge_id)::uuid, sqlc.arg(code_hash)::bytea,
                      sqlc.arg(new_token_hash)::bytea)
  AS r(session_id, mfa_auth_time, absolute_expires_at, factor_id, first_verification);

-- name: RemoveMFAFactor :one
SELECT r.provider::text AS provider, r.provider_factor_id::text AS provider_factor_id
FROM remove_mfa_factor(sqlc.arg(factor_id)::uuid) AS r(provider, provider_factor_id);

-- Tenants and members (design 5.5 and 5.7, migration 00005). Writes that change who is a
-- member, or with which role, go through these functions; app_runtime cannot write
-- memberships directly.

-- name: CreateTenant :one
SELECT r.tenant_id::uuid AS tenant_id, r.membership_id::uuid AS membership_id
FROM create_tenant(sqlc.arg(name)::text, sqlc.arg(slug)::text, sqlc.arg(max_tenants)::integer,
                   sqlc.narg(ip_prefix)::cidr) AS r(tenant_id, membership_id);

-- Under RLS, in the tenant's transaction. The lock serializes renames, so the audited
-- old name is the one replaced.
-- name: LockTenantName :one
SELECT t.name FROM tenants t WHERE t.id = $1 FOR UPDATE;

-- name: RenameTenant :one
UPDATE tenants SET name = sqlc.arg(name) WHERE id = sqlc.arg(id)
RETURNING id, slug, name;

-- A page of the tenant's members in membership order (UUIDv7, so by creation), which is
-- unique and stable. Under RLS; the WHERE on tenant_id is defence in depth.
-- name: ListMembers :many
SELECT m.id, m.user_id, u.email::text AS email, u.display_name, m.role, m.created_at
FROM memberships m
JOIN users u ON u.id = m.user_id
WHERE m.tenant_id = sqlc.arg(tenant_id)
  AND (sqlc.narg(after)::uuid IS NULL OR m.id > sqlc.narg(after)::uuid)
ORDER BY m.id
LIMIT sqlc.arg(page_size);

-- name: MemberByUser :one
SELECT m.id, m.user_id, u.email::text AS email, u.display_name, m.role, m.created_at
FROM memberships m
JOIN users u ON u.id = m.user_id
WHERE m.tenant_id = sqlc.arg(tenant_id) AND m.user_id = sqlc.arg(user_id);

-- name: ChangeMemberRole :exec
SELECT change_member_role(sqlc.arg(member)::uuid, sqlc.arg(role)::text, sqlc.arg(expected_actor_role)::text,
                          sqlc.arg(expected_member_role)::text, sqlc.narg(ip_prefix)::cidr);

-- name: RemoveMember :exec
SELECT remove_member(sqlc.arg(member)::uuid, sqlc.arg(expected_actor_role)::text,
                     sqlc.arg(expected_member_role)::text, sqlc.narg(ip_prefix)::cidr);

-- name: LeaveTenant :exec
SELECT leave_tenant(sqlc.narg(ip_prefix)::cidr);
