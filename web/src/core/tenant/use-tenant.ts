import { useParams } from "@tanstack/react-router";
import { type CurrentTenant, useGetCurrentTenant } from "@/gen/api";

/** useRouteTenantId is the $tenantId of the active route (/t/$tenantId/…). */
export function useRouteTenantId(): string {
  const params = useParams({ strict: false });
  if (!params.tenantId) {
    throw new Error("useRouteTenantId outside /t/$tenantId");
  }
  return params.tenantId;
}

/**
 * useCurrentTenant is GET /tenant for the tenant of the route: name, role and
 * permissions. The tenant layout has already loaded it, so it is cached.
 */
export function useCurrentTenant() {
  return useGetCurrentTenant();
}

/**
 * can says whether the role has the permission, to hide what it does not allow. It is
 * only UX: the API checks the permission again in every use case.
 */
export function can(tenant: Pick<CurrentTenant, "permissions"> | undefined, permission?: string) {
  return permission === undefined || (tenant?.permissions.includes(permission) ?? false);
}
