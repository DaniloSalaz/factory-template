import { ApiError } from "@/core/api/problem";
import { paths } from "@/core/paths";
import type { LastTenantStore } from "@/core/tenant/last-tenant";
import type { Notices } from "./notices";

/** What the platform does with an error, whoever made the request. */
export type ErrorAction =
  | { kind: "login"; returnTo: string }
  | { kind: "mfa_verify"; returnTo: string }
  | { kind: "mfa_enroll"; returnTo: string }
  | { kind: "tenant_forbidden" }
  | { kind: "rate_limited"; retryAfterSeconds: number | undefined };

export type Location = { pathname: string; href: string };

/**
 * isHandledGlobally says whether the error is one the platform deals with by itself
 * (sign in again, MFA, another tenant, wait): screens then show nothing of their own.
 */
export function isHandledGlobally(error: unknown): boolean {
  if (!(error instanceof ApiError)) {
    return false;
  }
  if (error.status === 401 || error.status === 429) {
    return true;
  }
  return (
    error.status === 403 &&
    (error.code === "auth.mfa_required" ||
      error.code === "auth.mfa_enrollment_required" ||
      error.code === "auth.tenant_forbidden")
  );
}

/**
 * actionFor maps an error to the platform's answer, or null when the screen that made the
 * request handles it (validation, conflicts…). It never sends to the screen the user is
 * already on, so a failing request there cannot loop.
 */
export function actionFor(error: unknown, at: Location): ErrorAction | null {
  if (!(error instanceof ApiError) || !isHandledGlobally(error)) {
    return null;
  }
  // A fragment can hold a secret (an invitation's token, 5.6.3): it never travels in
  // return_to, which the server receives and logs.
  const returnTo = withoutFragment(at.href);
  if (error.status === 401) {
    return at.pathname === paths.login ? null : { kind: "login", returnTo };
  }
  if (error.status === 429) {
    return { kind: "rate_limited", retryAfterSeconds: error.retryAfterSeconds };
  }
  switch (error.code) {
    case "auth.mfa_required":
      return at.pathname === paths.mfaVerify ? null : { kind: "mfa_verify", returnTo };
    case "auth.mfa_enrollment_required":
      return at.pathname === paths.mfaEnroll ? null : { kind: "mfa_enroll", returnTo };
    default:
      return { kind: "tenant_forbidden" };
  }
}

function withoutFragment(href: string): string {
  const i = href.indexOf("#");
  return i === -1 ? href : href.slice(0, i);
}

export type Destination =
  | { to: typeof paths.login; search: { return_to: string } }
  | { to: typeof paths.mfaVerify; search: { return_to: string } }
  | { to: typeof paths.mfaEnroll; search: { return_to: string } }
  | { to: typeof paths.tenants; search: { notice: "tenant_forbidden" } };

export type ErrorHandlerDeps = {
  location: () => Location;
  navigate: (destination: Destination) => void;
  /** The tenant of the active route, if any. */
  routeTenant: () => string | undefined;
  lastTenant: LastTenantStore;
  notices: Notices;
};

/**
 * createErrorHandler returns the one function every failed query and mutation goes
 * through (QueryCache and MutationCache onError, core/api/query-client.ts).
 */
export function createErrorHandler(deps: ErrorHandlerDeps): (error: unknown) => void {
  return (error) => {
    const action = actionFor(error, deps.location());
    if (!action) {
      return;
    }
    switch (action.kind) {
      case "login":
        deps.navigate({ to: paths.login, search: { return_to: action.returnTo } });
        return;
      case "mfa_verify":
        deps.navigate({ to: paths.mfaVerify, search: { return_to: action.returnTo } });
        return;
      case "mfa_enroll":
        deps.navigate({ to: paths.mfaEnroll, search: { return_to: action.returnTo } });
        return;
      case "tenant_forbidden": {
        const tenant = deps.routeTenant();
        if (tenant && deps.lastTenant.get() === tenant) {
          deps.lastTenant.clear();
        }
        deps.navigate({ to: paths.tenants, search: { notice: "tenant_forbidden" } });
        return;
      }
      case "rate_limited":
        deps.notices.show({ kind: "rate_limited", retryAfterSeconds: action.retryAfterSeconds });
        return;
    }
  };
}
