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

-- name: CreateSession :one
INSERT INTO sessions (token_hash, user_id, auth_time, mfa_auth_time, last_seen_at,
                      idle_expires_at, absolute_expires_at, ip_prefix, user_agent, provider_session_id)
VALUES (sqlc.arg(token_hash), sqlc.arg(user_id), sqlc.arg(auth_time), sqlc.narg(mfa_auth_time),
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
