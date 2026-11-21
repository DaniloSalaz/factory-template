// Test data, seeded as the local superuser `factory` exactly as docs/runbooks/auth.md does
// (option B: a user and a session without AuthKit). Only for the local database.
import { execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { fileURLToPath } from "node:url";

const repoRoot = fileURLToPath(new URL("../..", import.meta.url));

/** sql runs statements as `factory` and returns the output (-At: unaligned, no headers). */
export function sql(statements: string, vars: Record<string, string> = {}): string {
  const args = ["compose", "exec", "-T", "postgres", "psql", "-U", "factory", "-d", "factory"];
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

/** newUser creates a user with a one-hour session; the database keeps only its hash. */
export function newUser(name: string): User {
  const email = `${name}.${run}@example.test`;
  const token = randomBytes(32).toString("base64url");
  const id = sql(
    `INSERT INTO users (email, email_verified, display_name) VALUES (:'email', true, :'name') RETURNING id;
     INSERT INTO sessions (token_hash, user_id, auth_time, last_seen_at, idle_expires_at, absolute_expires_at)
     SELECT sha256(convert_to(:'token', 'UTF8')), id, now(), now(), now() + interval '1 hour', now() + interval '1 hour'
     FROM users WHERE email = :'email';`,
    { email, name, token },
  ).split("\n")[0];
  if (!id) throw new Error("newUser: no id");
  return { id, email, token };
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
