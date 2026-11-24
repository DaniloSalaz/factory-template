import type { Me } from "@/gen/api";

// The tenant lives in the URL (/t/$tenantId). The only copy kept elsewhere is the last
// one used, and only to redirect from / (auth-y-tenancy §10.1).
const KEY = "platform.lastTenantId";

export type LastTenantStore = {
  get(): string | undefined;
  set(tenantId: string): void;
  clear(): void;
};

/**
 * createLastTenantStore keeps the last tenant in localStorage. Storage may be missing or
 * throw (private windows, blocked site data): then it simply remembers nothing.
 */
export function createLastTenantStore(
  storage: () => Storage = () => window.localStorage,
): LastTenantStore {
  return {
    get() {
      try {
        return storage().getItem(KEY) ?? undefined;
      } catch {
        return undefined;
      }
    },
    set(tenantId) {
      try {
        storage().setItem(KEY, tenantId);
      } catch {
        // Nothing to do: the redirect from / falls back to the tenant picker.
      }
    },
    clear() {
      try {
        storage().removeItem(KEY);
      } catch {
        // Same as above.
      }
    },
  };
}

/**
 * homeDestination decides where / goes: the last tenant used if GET /me still lists it as
 * active, otherwise the tenant picker. A last tenant that is no longer valid is forgotten.
 */
export function homeDestination(
  me: Pick<Me, "memberships">,
  store: LastTenantStore,
): { to: "/t/$tenantId"; tenantId: string } | { to: "/tenants" } {
  const last = store.get();
  if (!last) {
    return { to: "/tenants" };
  }
  const m = me.memberships.find((x) => x.tenant_id === last);
  if (m && m.tenant_status === "active") {
    return { to: "/t/$tenantId", tenantId: last };
  }
  store.clear();
  return { to: "/tenants" };
}
