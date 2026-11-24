import { describe, expect, it } from "vitest";
import { createLastTenantStore, homeDestination } from "@/core/tenant/last-tenant";
import type { Membership } from "@/gen/api";

function storage(): Storage {
  const m = new Map<string, string>();
  return {
    get length() {
      return m.size;
    },
    clear: () => m.clear(),
    getItem: (k) => m.get(k) ?? null,
    key: (i) => [...m.keys()][i] ?? null,
    removeItem: (k) => {
      m.delete(k);
    },
    setItem: (k, v) => {
      m.set(k, v);
    },
  };
}

const membership = (tenant_id: string, tenant_status: "active" | "suspended"): Membership => ({
  tenant_id,
  tenant_slug: tenant_id,
  tenant_name: tenant_id,
  tenant_status,
  role: "admin",
  mfa_required: false,
});

describe("last tenant store", () => {
  it("remembers, replaces and forgets one tenant", () => {
    const st = storage();
    const store = createLastTenantStore(() => st);
    expect(store.get()).toBeUndefined();
    store.set("a");
    store.set("b");
    expect(store.get()).toBe("b");
    store.clear();
    expect(store.get()).toBeUndefined();
  });

  it("remembers nothing when storage is unavailable", () => {
    const store = createLastTenantStore(() => {
      throw new DOMException("blocked", "SecurityError");
    });
    expect(() => store.set("a")).not.toThrow();
    expect(store.get()).toBeUndefined();
    expect(() => store.clear()).not.toThrow();
  });
});

describe("homeDestination", () => {
  const make = (initial?: string) => {
    const st = storage();
    const store = createLastTenantStore(() => st);
    if (initial) store.set(initial);
    return store;
  };

  it("goes to the last tenant if GET /me still lists it as active", () => {
    const store = make("a");
    const me = { memberships: [membership("b", "active"), membership("a", "active")] };
    expect(homeDestination(me, store)).toEqual({ to: "/t/$tenantId", tenantId: "a" });
    expect(store.get()).toBe("a");
  });

  it("goes to the selector without a last tenant", () => {
    expect(homeDestination({ memberships: [membership("a", "active")] }, make())).toEqual({
      to: "/tenants",
    });
  });

  it("forgets a last tenant the user no longer belongs to", () => {
    const store = make("gone");
    expect(homeDestination({ memberships: [membership("a", "active")] }, store)).toEqual({
      to: "/tenants",
    });
    expect(store.get()).toBeUndefined();
  });

  it("forgets a last tenant that is suspended", () => {
    const store = make("a");
    expect(homeDestination({ memberships: [membership("a", "suspended")] }, store)).toEqual({
      to: "/tenants",
    });
    expect(store.get()).toBeUndefined();
  });
});
