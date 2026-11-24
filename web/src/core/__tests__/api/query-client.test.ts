import { describe, expect, it, vi } from "vitest";
import { getGetCurrentTenantQueryKey, getGetMeQueryKey, getVerifyTOTPMutationKey } from "@/gen/api";
import { ApiError } from "@/platform/api/problem";
import { createQueryClient, tenantScopedHash } from "@/platform/api/query-client";

describe("tenantScopedHash", () => {
  it("keeps each tenant's cache of tenant operations apart", () => {
    let tenant = "a";
    const hash = tenantScopedHash(() => tenant);
    const inA = hash(getGetCurrentTenantQueryKey());
    tenant = "b";
    expect(hash(getGetCurrentTenantQueryKey())).not.toBe(inA);
  });

  it("shares the cache of operations without a tenant", () => {
    let tenant: string | undefined = "a";
    const hash = tenantScopedHash(() => tenant);
    const inA = hash(getGetMeQueryKey());
    tenant = undefined;
    expect(hash(getGetMeQueryKey())).toBe(inA);
  });
});

describe("createQueryClient", () => {
  it("sends every failed query and mutation to the error handler", async () => {
    const onError = vi.fn();
    const client = createQueryClient({ onError, routeTenant: () => undefined });
    const err = new ApiError(401, { code: "auth.session_invalid" });
    await client
      .fetchQuery({ queryKey: ["/me"], queryFn: () => Promise.reject(err), retry: false })
      .catch(() => undefined);
    await client
      .getMutationCache()
      .build(client, { mutationFn: () => Promise.reject(err) })
      .execute(undefined)
      .catch(() => undefined);
    expect(onError).toHaveBeenCalledTimes(2);
    expect(onError).toHaveBeenCalledWith(err);
  });

  it("does not retry answers below 500", () => {
    const client = createQueryClient({ onError: () => undefined, routeTenant: () => undefined });
    const retry = client.getDefaultOptions().queries?.retry;
    if (typeof retry !== "function") throw new Error("retry is not a function");
    expect(retry(0, new ApiError(403, {}))).toBe(false);
    expect(retry(0, new ApiError(503, {}))).toBe(true);
    expect(retry(0, new TypeError("Failed to fetch"))).toBe(true);
    expect(retry(2, new ApiError(503, {}))).toBe(false);
  });

  it("invalidates the whole cache once a code is verified (the session is replaced)", async () => {
    const client = createQueryClient({ onError: () => undefined, routeTenant: () => undefined });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    await client
      .getMutationCache()
      .build(client, { mutationKey: ["createTenant"], mutationFn: async () => "ok" })
      .execute(undefined);
    expect(invalidate).not.toHaveBeenCalled();
    await client
      .getMutationCache()
      .build(client, { mutationKey: getVerifyTOTPMutationKey(), mutationFn: async () => "ok" })
      .execute(undefined);
    expect(invalidate).toHaveBeenCalledWith();
  });
});
