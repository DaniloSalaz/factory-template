import { routeSecurity } from "@/gen/routeSecurity";

type Template = { method: string; segments: string[]; params: number; tenant: boolean };

const templates: Template[] = Object.entries(routeSecurity).map(([key, s]) => {
  const [method = "", path = ""] = key.split(" ");
  const segments = path.split("/");
  return {
    method,
    segments,
    params: segments.filter(isParam).length,
    tenant: s.tenant,
  };
});

function isParam(segment: string): boolean {
  return segment.startsWith("{") && segment.endsWith("}");
}

/**
 * requiresTenant says whether the contract's operation for this method and path (as the
 * generated client builds it, query string included) requires X-Tenant-ID. A literal
 * segment wins over a parameter, as in the API's ServeMux (/tenant/members/me). A path
 * the contract does not declare requires none.
 */
export function requiresTenant(method: string, path: string): boolean {
  const segments = (path.split("?")[0] ?? "").split("/");
  const m = method.toUpperCase();
  let best: Template | undefined;
  for (const t of templates) {
    if (t.method !== m || t.segments.length !== segments.length) {
      continue;
    }
    const matches = t.segments.every((s, i) => isParam(s) || s === segments[i]);
    if (matches && (!best || t.params < best.params)) {
      best = t;
    }
  }
  return best?.tenant ?? false;
}
