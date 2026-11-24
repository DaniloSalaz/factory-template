import { index, layout, physical, rootRoute, route } from "@tanstack/virtual-file-routes";

// The route tree (read by the TanStack Router plugin, vite.config.ts). Paths are relative
// to src/. The core's routes are listed here; the app's are file-based, under
// src/app/routes, and mounted inside /t/$tenantId, so they get its layout (the tenant's
// navigation and GET /tenant) and their URLs are /t/$tenantId/<file>.
export const routes = rootRoute("core/routes/root.tsx", [
  route("/login", "core/routes/login.tsx"),
  route("/invite", "core/routes/invite.tsx"),
  layout("shell", "core/routes/shell.tsx", [
    index("core/routes/home.tsx"),
    route("/tenants", "core/routes/tenants.tsx"),
    route("/account/mfa", [
      index("core/routes/mfa.tsx"),
      route("/verify", "core/routes/mfa-verify.tsx"),
      route("/enroll", "core/routes/mfa-enroll.tsx"),
    ]),
    route("/t/$tenantId", "core/routes/tenant.tsx", [
      index("core/routes/tenant-home.tsx"),
      route("/members", "core/routes/members.tsx"),
      route("/invitations", "core/routes/invitations.tsx"),
      physical("app/routes"),
    ]),
  ]),
]);
