import { ApiError, parseProblem, parseRetryAfter } from "./problem";
import { requiresTenant } from "./route-security";

// Base URL of the API (design, section 10): "/api" in development, where the Vite proxy
// strips it, and the API's own origin in production. The backend has no /api prefix.
const baseUrl = import.meta.env.VITE_API_BASE_URL.replace(/\/$/, "");

/** apiUrl turns a path of the generated client into a URL of the API. */
export function apiUrl(path: string): string {
  return `${baseUrl}${path}`;
}

/** Reads the tenant of the active route; undefined outside /t/$tenantId. */
export type TenantSource = () => string | undefined;

// The only state of this module: who to ask for the active route's tenant. The router
// sets it once at start-up (platform/bootstrap.ts); the tenant itself is never stored.
let tenantSource: TenantSource = () => undefined;

export function setTenantSource(source: TenantSource): void {
  tenantSource = source;
}

/** TenantMissingError: a tenant operation was called outside /t/$tenantId (a bug). */
export class TenantMissingError extends Error {
  constructor(method: string, path: string) {
    super(`${method} ${path} requires a tenant, but the active route has none`);
    this.name = "TenantMissingError";
  }
}

/**
 * apiFetch is the fetch used by the generated client (orval mutator). It sends the
 * session cookie also when the API is on another origin of the same site, adds
 * X-Tenant-ID (the tenant of the active route) only to the operations whose contract
 * requires it, and throws an ApiError for any non-2xx answer. Errors are handled in
 * one place (platform/errors), never by each caller.
 */
export async function apiFetch<T>(path: string, init: RequestInit): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  if (requiresTenant(method, path)) {
    const tenantId = tenantSource();
    if (!tenantId) {
      throw new TenantMissingError(method, path);
    }
    headers.set("X-Tenant-ID", tenantId);
  }
  const res = await fetch(apiUrl(path), { ...init, headers, credentials: "include" });
  const text = [204, 205, 304].includes(res.status) ? "" : await res.text();
  if (!res.ok) {
    throw new ApiError(
      res.status,
      parseProblem(text),
      parseRetryAfter(res.headers.get("Retry-After")),
    );
  }
  return (text ? JSON.parse(text) : undefined) as T;
}

/** The error type of the generated hooks: apiFetch throws ApiError (or a network error). */
export type ErrorType<_Problem> = ApiError | Error;
