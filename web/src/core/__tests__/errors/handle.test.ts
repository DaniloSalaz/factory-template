import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/core/api/problem";
import {
  actionFor,
  createErrorHandler,
  type ErrorHandlerDeps,
  isHandledGlobally,
} from "@/core/errors/handle";
import { createNotices } from "@/core/errors/notices";
import type { LastTenantStore } from "@/core/tenant/last-tenant";

const at = { pathname: "/t/abc", href: "/t/abc?tab=1" };
const problem = (status: number, code?: string, retry?: number) =>
  new ApiError(status, code ? { code, title: "x" } : {}, retry);

function memoryStore(initial?: string): LastTenantStore {
  let value = initial;
  return {
    get: () => value,
    set: (v) => {
      value = v;
    },
    clear: () => {
      value = undefined;
    },
  };
}

function deps(over: Partial<Omit<ErrorHandlerDeps, "navigate">> = {}) {
  const navigate = vi.fn<ErrorHandlerDeps["navigate"]>();
  const d = {
    location: () => at,
    routeTenant: () => "abc",
    lastTenant: memoryStore("abc"),
    notices: createNotices(),
    ...over,
    navigate,
  };
  return { ...d, handle: createErrorHandler(d) };
}

describe("actionFor", () => {
  it("sends a 401 to /login with the current route as return_to", () => {
    expect(actionFor(problem(401, "auth.session_invalid"), at)).toEqual({
      kind: "login",
      returnTo: "/t/abc?tab=1",
    });
  });

  it("never puts the URL's fragment in return_to", () => {
    const invite = { pathname: "/invite", href: "/invite#token=secret" };
    expect(actionFor(problem(401), invite)).toEqual({ kind: "login", returnTo: "/invite" });
    expect(actionFor(problem(403, "auth.mfa_required"), invite)).toEqual({
      kind: "mfa_verify",
      returnTo: "/invite",
    });
  });

  it("maps the 403s the core handles by their code", () => {
    expect(actionFor(problem(403, "auth.mfa_required"), at)).toEqual({
      kind: "mfa_verify",
      returnTo: at.href,
    });
    expect(actionFor(problem(403, "auth.mfa_enrollment_required"), at)).toEqual({
      kind: "mfa_enroll",
      returnTo: at.href,
    });
    expect(actionFor(problem(403, "auth.tenant_forbidden"), at)).toEqual({
      kind: "tenant_forbidden",
    });
  });

  it("passes on the wait of a 429 when it comes, and none when it does not", () => {
    expect(actionFor(problem(429, "mfa.rate_limited", 30), at)).toEqual({
      kind: "rate_limited",
      retryAfterSeconds: 30,
    });
    expect(actionFor(problem(429, "rate_limit.exceeded"), at)).toEqual({
      kind: "rate_limited",
      retryAfterSeconds: undefined,
    });
  });

  it("leaves every other error to the screen", () => {
    for (const e of [
      problem(403, "authz.denied"),
      problem(403, "mfa.last_factor_required"),
      problem(422, "tenant.slug_invalid"),
      problem(409, "tenant.slug_taken"),
      problem(500),
      new TypeError("Failed to fetch"),
      "not an error",
    ]) {
      expect(actionFor(e, at)).toBeNull();
      expect(isHandledGlobally(e)).toBe(false);
    }
  });

  it("never sends to the screen the user is on", () => {
    expect(
      actionFor(problem(401), { pathname: "/login", href: "/login?return_to=%2F" }),
    ).toBeNull();
    const verify = { pathname: "/account/mfa/verify", href: "/account/mfa/verify?return_to=%2F" };
    expect(actionFor(problem(403, "auth.mfa_required"), verify)).toBeNull();
    const enroll = { pathname: "/account/mfa/enroll", href: "/account/mfa/enroll" };
    expect(actionFor(problem(403, "auth.mfa_enrollment_required"), enroll)).toBeNull();
  });
});

describe("createErrorHandler", () => {
  it("navigates to /login, the MFA screens and the selector", () => {
    const d = deps();
    d.handle(problem(401));
    d.handle(problem(403, "auth.mfa_required"));
    d.handle(problem(403, "auth.mfa_enrollment_required"));
    expect(d.navigate.mock.calls.map((c) => c[0])).toEqual([
      { to: "/login", search: { return_to: at.href } },
      { to: "/account/mfa/verify", search: { return_to: at.href } },
      { to: "/account/mfa/enroll", search: { return_to: at.href } },
    ]);
  });

  it("forgets the last tenant when it is the forbidden one, and goes to the selector", () => {
    const d = deps();
    d.handle(problem(403, "auth.tenant_forbidden"));
    expect(d.lastTenant.get()).toBeUndefined();
    expect(d.navigate).toHaveBeenCalledWith({
      to: "/tenants",
      search: { notice: "tenant_forbidden" },
    });
  });

  it("keeps the last tenant when another one is forbidden", () => {
    const d = deps({ routeTenant: () => "other", lastTenant: memoryStore("abc") });
    d.handle(problem(403, "auth.tenant_forbidden"));
    expect(d.lastTenant.get()).toBe("abc");
  });

  it("shows a notice for a 429 without navigating", () => {
    const d = deps();
    d.handle(problem(429, "rate_limit.exceeded", 90));
    expect(d.notices.current()).toEqual({ kind: "rate_limited", retryAfterSeconds: 90 });
    expect(d.navigate).not.toHaveBeenCalled();
  });

  it("does nothing with errors the screen handles", () => {
    const d = deps();
    d.handle(problem(422, "mfa.code_invalid"));
    expect(d.navigate).not.toHaveBeenCalled();
    expect(d.notices.current()).toBeUndefined();
  });
});
