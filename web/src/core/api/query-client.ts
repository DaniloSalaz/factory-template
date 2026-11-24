import {
  hashKey,
  MutationCache,
  QueryCache,
  QueryClient,
  type QueryKey,
} from "@tanstack/react-query";
import { getVerifyTOTPMutationKey } from "@/gen/api";
import { ApiError } from "./problem";
import { requiresTenant } from "./route-security";

export type QueryClientDeps = {
  /** The core's error handler (core/errors/handle.ts). */
  onError: (error: unknown) => void;
  /** The tenant of the active route, the same one apiFetch sends as X-Tenant-ID. */
  routeTenant: () => string | undefined;
};

/**
 * tenantScopedHash keeps the cache of each tenant apart: the generated keys of tenant
 * operations (["/tenant"]) do not name the tenant, which travels in X-Tenant-ID, so the
 * hash adds it. Without this, /t/B would first show the data cached for /t/A.
 */
export function tenantScopedHash(routeTenant: () => string | undefined) {
  return (key: QueryKey): string => {
    const path = key[0];
    if (typeof path === "string" && requiresTenant("GET", path)) {
      return hashKey([{ tenant: routeTenant() ?? null }, ...key]);
    }
    return hashKey(key);
  };
}

/** Errors that a retry cannot fix: every answer of the API below 500. */
function retryable(error: unknown): boolean {
  return !(error instanceof ApiError) || error.status >= 500;
}

/**
 * createQueryClient wires every query and mutation to the core's error handler, the
 * one place that decides what 401, the MFA answers, a forbidden tenant and 429 do.
 */
export function createQueryClient(deps: QueryClientDeps): QueryClient {
  const verifyKey = getVerifyTOTPMutationKey()[0];
  const client: QueryClient = new QueryClient({
    queryCache: new QueryCache({ onError: (error) => deps.onError(error) }),
    mutationCache: new MutationCache({
      onError: (error) => deps.onError(error),
      onSuccess: (_data, _variables, _result, mutation) => {
        // A verified code replaces the session (new cookie): nothing cached was read with it.
        if (mutation.options.mutationKey?.[0] === verifyKey) {
          return client.invalidateQueries();
        }
        return undefined;
      },
    }),
    defaultOptions: {
      queries: {
        queryKeyHashFn: tenantScopedHash(deps.routeTenant),
        retry: (count, error) => retryable(error) && count < 2,
      },
      mutations: { retry: false },
    },
  });
  return client;
}
