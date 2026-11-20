import { index, layout, physical, rootRoute, route } from "@tanstack/virtual-file-routes";

// The route tree (read by the TanStack Router plugin, vite.config.ts). Paths are relative
// to src/. The platform's routes are listed here; the app's are file-based, under
// src/app/routes, and mounted inside /t/$tenantId, so they get its layout (the tenant's
// navigation and GET /tenant) and their URLs are /t/$tenantId/<file>.
export const routes = rootRoute("platform/routes/root.tsx", [
  route("/login", "platform/routes/login.tsx"),
  layout("shell", "platform/routes/shell.tsx", [
    index("platform/routes/home.tsx"),
    route("/tenants", "platform/routes/tenants.tsx"),
    route("/account/mfa", [
      index("platform/routes/mfa.tsx"),
      route("/verify", "platform/routes/mfa-verify.tsx"),
      route("/enroll", "platform/routes/mfa-enroll.tsx"),
    ]),
    route("/t/$tenantId", "platform/routes/tenant.tsx", [
      index("platform/routes/tenant-home.tsx"),
      physical("app/routes"),
    ]),
  ]),
]);
