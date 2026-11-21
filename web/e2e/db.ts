// Test data, seeded as the superuser `factory` exactly as docs/platform/runbooks/auth.md does
// (option B: a user and a session without AuthKit). Only for the local database, or the
// CI job's: there E2E_PG_CONTAINER names its Postgres service container.
import { execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { fileURLToPath } from "node:url";

const repoRoot = fileURLToPath(new URL("../..", import.meta.url));

/** sql runs statements as `factory` and returns the output (-At: unaligned, no headers). */
export function sql(statements: string, vars: Record<string, string> = {}): string {
  const container = process.env["E2E_PG_CONTAINER"];
  const args = container
    ? ["exec", "-i", container, "psql", "-U", "factory", "-d", "factory"]
    : ["compose", "exec", "-T", "postgres", "psql", "-U", "factory", "-d", "factory"];
  args.push("-v", "ON_ERROR_STOP=1", "-At");
  for (const [k, v] of Object.entries(vars)) args.push("-v", `${k}=${v}`);
  return execFileSync("docker", args, {
    cwd: repoRoot,
    input: statements,
    encoding: "utf8",
  }).trim();
}

/** The marker of every row this suite creates, so cleanup finds them. */
export const run = `e2e${Date.now().toString(36)}`;

export type User = { id: string; email: string; token: string };

/** emailOf is the address newUser gives name in this run. */
export const emailOf = (name: string) => `${name}.${run}@example.test`;

/**
 * newUser creates a user with a one-hour session; the database keeps only its hash.
 * session: false creates no session (someone who will sign in later). mfa: true gives
 * the session an MFA of now, as mark_session_mfa would: owner requires it.
 */
export function newUser(name: string, opts: { session?: boolean; mfa?: boolean } = {}): User {
  const email = emailOf(name);
  const token = randomBytes(32).toString("base64url");
  const id = sql(
    `INSERT INTO users (email, email_verified, display_name) VALUES (:'email', true, :'name') RETURNING id;`,
    { email, name },
  ).split("\n")[0];
  if (!id) throw new Error("newUser: no id");
  const user = { id, email, token };
  if (opts.session !== false) newSession(user, opts.mfa ?? false);
  return user;
}

/** newSession gives user a one-hour session with user.token (the cookie's value). */
export function newSession(user: User, mfa = false): void {
  sql(
    `INSERT INTO sessions (token_hash, user_id, auth_time, last_seen_at, idle_expires_at, absolute_expires_at, mfa_auth_time)
     VALUES (sha256(convert_to(:'token', 'UTF8')), :'id', now(), now(), now() + interval '1 hour',
             now() + interval '1 hour', CASE WHEN :'mfa' = 'true' THEN now() END);`,
    { token: user.token, id: user.id, mfa: String(mfa) },
  );
}

/**
 * knownInviteToken replaces the token of the pending invitation of email in tenant with
 * a new one and returns it: the real one only reaches the development log
 * (docs/platform/runbooks/auth.md), so the test cannot read it.
 */
export function knownInviteToken(tenant: string, email: string): string {
  const token = randomBytes(32).toString("base64url");
  const n = sql(
    `UPDATE invitations SET token_hash = sha256(convert_to(:'token', 'UTF8'))
     WHERE tenant_id = :'t' AND email = :'e' AND accepted_at IS NULL AND revoked_at IS NULL
     RETURNING id;`,
    { token, t: tenant, e: email },
  );
  if (!n) throw new Error(`knownInviteToken: no pending invitation of ${email}`);
  return token;
}

/** newTenant creates a tenant with memberships, as the runbook's preparation does. */
export function newTenant(
  slug: string,
  members: { user: User; role: string }[],
  status: "active" | "suspended" = "active",
): string {
  const fullSlug = `${slug}-${run}`;
  const id = sql(
    `INSERT INTO tenants (name, slug, status) VALUES (:'name', :'slug', :'status') RETURNING id;`,
    { name: `${slug} ${run}`, slug: fullSlug, status },
  ).split("\n")[0];
  if (!id) throw new Error("newTenant: no id");
  for (const m of members) {
    sql(`INSERT INTO memberships (tenant_id, user_id, role) VALUES (:'t', :'u', :'r');`, {
      t: id,
      u: m.user.id,
      r: m.role,
    });
  }
  return id;
}

/** cleanup deletes everything this run created (the superuser bypasses RLS). */
export function cleanup(): void {
  sql(
    `DELETE FROM audit_log WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE '%' || :'run');
     DELETE FROM invitations WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE '%' || :'run');
     DELETE FROM memberships WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE '%' || :'run');
     DELETE FROM tenants WHERE slug LIKE '%' || :'run';
     DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE email LIKE '%.' || :'run' || '@example.test');
     DELETE FROM audit_log WHERE actor_user_id IN (SELECT id FROM users WHERE email LIKE '%.' || :'run' || '@example.test');
     DELETE FROM users WHERE email LIKE '%.' || :'run' || '@example.test';`,
    { run },
  );
}
