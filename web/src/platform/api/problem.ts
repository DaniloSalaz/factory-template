import type { FieldError, Problem } from "@/gen/api";

/**
 * ApiError is every non-2xx answer of the API, parsed from its problem details (RFC 9457).
 * `code` is the API's stable error code (auth.mfa_required, tenant.slug_taken…); the UI
 * decides by it, never by the title or the detail.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string | undefined;
  readonly fieldErrors: readonly FieldError[];
  readonly requestId: string | undefined;
  /** Seconds to wait, from Retry-After, when the API sends it. */
  readonly retryAfterSeconds: number | undefined;

  constructor(status: number, problem: Partial<Problem>, retryAfterSeconds?: number) {
    super(problem.title ?? `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = problem.code;
    this.fieldErrors = problem.errors ?? [];
    this.requestId = problem.request_id;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

/** parseProblem reads a problem details body; anything else yields an empty problem. */
export function parseProblem(body: string): Partial<Problem> {
  if (!body) {
    return {};
  }
  try {
    const value: unknown = JSON.parse(body);
    if (typeof value !== "object" || value === null || Array.isArray(value)) {
      return {};
    }
    const p = value as Record<string, unknown>;
    const problem: Partial<Problem> = {};
    if (typeof p["title"] === "string") problem.title = p["title"];
    if (typeof p["detail"] === "string") problem.detail = p["detail"];
    if (typeof p["code"] === "string") problem.code = p["code"];
    if (typeof p["request_id"] === "string") problem.request_id = p["request_id"];
    if (Array.isArray(p["errors"])) problem.errors = p["errors"].filter(isFieldError);
    return problem;
  } catch {
    return {};
  }
}

function isFieldError(v: unknown): v is FieldError {
  if (typeof v !== "object" || v === null) {
    return false;
  }
  const f = v as Record<string, unknown>;
  return typeof f["field"] === "string" && typeof f["code"] === "string";
}

/** parseRetryAfter reads Retry-After as seconds or as an HTTP date (RFC 9110, 10.2.3). */
export function parseRetryAfter(
  value: string | null,
  now: number = Date.now(),
): number | undefined {
  if (!value) {
    return undefined;
  }
  if (/^\d+$/.test(value.trim())) {
    return Number(value.trim());
  }
  const at = Date.parse(value);
  if (Number.isNaN(at)) {
    return undefined;
  }
  return Math.max(0, Math.ceil((at - now) / 1000));
}
