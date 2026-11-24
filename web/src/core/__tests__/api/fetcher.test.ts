import { afterEach, describe, expect, it, vi } from "vitest";
import { getCurrentTenant, getMe, leaveTenant, removeMember } from "@/gen/api";
import { apiFetch, setTenantSource, TenantMissingError } from "@/platform/api/fetcher";
import { ApiError } from "@/platform/api/problem";
import { requiresTenant } from "@/platform/api/route-security";

const tenant = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b";

function mockFetch(status = 200, body = "{}", headers: Record<string, string> = {}) {
  const fn = vi.fn<typeof fetch>(
    async () => new Response(status === 204 ? null : body, { status, headers }),
  );
  vi.stubGlobal("fetch", fn);
  return fn;
}

function sent(fn: ReturnType<typeof mockFetch>) {
  const [url, init] = fn.mock.calls[0] ?? [];
  return { url, init, headers: new Headers(init?.headers) };
}

afterEach(() => {
  setTenantSource(() => undefined);
  vi.unstubAllGlobals();
});

describe("requiresTenant", () => {
  it("follows the contract's security", () => {
    expect(requiresTenant("GET", "/tenant")).toBe(true);
    expect(requiresTenant("PATCH", "/tenant")).toBe(true);
    expect(requiresTenant("GET", "/tenant/members?limit=2&cursor=x")).toBe(true);
    expect(requiresTenant("DELETE", `/tenant/members/${tenant}`)).toBe(true);
    expect(requiresTenant("DELETE", "/tenant/members/me")).toBe(true);
    expect(requiresTenant("POST", `/invitations/${tenant}/resend`)).toBe(true);
    expect(requiresTenant("GET", "/me")).toBe(false);
    expect(requiresTenant("POST", "/tenants")).toBe(false);
    expect(requiresTenant("POST", "/invitations/accept")).toBe(false);
    expect(requiresTenant("DELETE", `/me/mfa/totp/${tenant}`)).toBe(false);
    expect(requiresTenant("POST", "/auth/logout")).toBe(false);
    expect(requiresTenant("GET", "/healthz")).toBe(false);
  });

  it("requires none for a path or method the contract does not declare", () => {
    expect(requiresTenant("GET", "/nope")).toBe(false);
    expect(requiresTenant("PUT", "/tenant")).toBe(false);
  });
});

describe("apiFetch", () => {
  it("sends X-Tenant-ID from the active route to operations that require a tenant", async () => {
    setTenantSource(() => tenant);
    const fn = mockFetch(200, '{"id":"x"}');
    await getCurrentTenant();
    const s = sent(fn);
    expect(s.url).toBe("/api/tenant");
    expect(s.headers.get("X-Tenant-ID")).toBe(tenant);
    expect(s.init?.credentials).toBe("include");
  });

  it("also with path parameters and literal segments", async () => {
    setTenantSource(() => tenant);
    const fn = mockFetch(204);
    await removeMember("0190a1b2-0000-7000-8000-000000000001");
    await leaveTenant();
    expect(fn).toHaveBeenCalledTimes(2);
    for (const [, init] of fn.mock.calls) {
      expect(new Headers(init?.headers).get("X-Tenant-ID")).toBe(tenant);
    }
  });

  it("does not send it to operations without a tenant, even inside /t/$tenantId", async () => {
    setTenantSource(() => tenant);
    const fn = mockFetch(200, '{"memberships":[]}');
    await getMe();
    expect(sent(fn).headers.has("X-Tenant-ID")).toBe(false);
  });

  it("refuses a tenant operation outside /t/$tenantId instead of sending it without", async () => {
    const fn = mockFetch();
    await expect(getCurrentTenant()).rejects.toBeInstanceOf(TenantMissingError);
    expect(fn).not.toHaveBeenCalled();
  });

  it("turns problem details into an ApiError with its code and wait", async () => {
    mockFetch(
      429,
      JSON.stringify({
        title: "Too many",
        status: 429,
        code: "mfa.rate_limited",
        request_id: "r1",
      }),
      { "Content-Type": "application/problem+json", "Retry-After": "42" },
    );
    const err: unknown = await apiFetch("/me/mfa/totp/verify", { method: "POST" }).catch(
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      status: 429,
      code: "mfa.rate_limited",
      requestId: "r1",
      retryAfterSeconds: 42,
    });
  });

  it("keeps the status when the error body is not a problem", async () => {
    mockFetch(502, "<html>bad gateway</html>");
    const err: unknown = await apiFetch("/me", { method: "GET" }).catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 502, code: undefined });
  });

  it("resolves a 204 with no body", async () => {
    mockFetch(204);
    await expect(apiFetch("/auth/logout", { method: "POST" })).resolves.toBeUndefined();
  });
});
