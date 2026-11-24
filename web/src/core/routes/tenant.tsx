import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { useEffect } from "react";
import { registeredApp } from "@/core/app-registry";
import type { TenantNavItem } from "@/core/app-types";
import { useCore } from "@/core/bootstrap";
import { ErrorText } from "@/core/components/ErrorText";
import { can, useCurrentTenant } from "@/core/tenant/use-tenant";
import { texts } from "@/core/texts";
import { buttonVariants } from "@/core/ui/button";

// The layout of /t/$tenantId and of every app screen below it. GET /tenant checks
// membership, the tenant's status and the role's MFA: its 403s go through the error
// handler (selector, MFA verification or enrollment), so children render only when it
// succeeds. That is also when the tenant becomes the last one used.
export const Route = createFileRoute("/_shell/t/$tenantId")({
  component: TenantLayout,
});

// The core's own screens of a tenant, before the app's.
const coreNav: readonly TenantNavItem[] = [
  { label: texts.shell.members, to: "/t/$tenantId/members", permission: "members:read" },
  {
    label: texts.shell.invitations,
    to: "/t/$tenantId/invitations",
    permission: "members:invite",
  },
];

function TenantLayout() {
  const { tenantId } = Route.useParams();
  const tenant = useCurrentTenant();
  const { lastTenant } = useCore();

  useEffect(() => {
    if (tenant.isSuccess && tenant.data.id === tenantId) {
      lastTenant.set(tenantId);
    }
  }, [tenant.isSuccess, tenant.data, tenantId, lastTenant]);

  if (tenant.isPending) {
    return <p>{texts.common.loading}</p>;
  }
  if (tenant.isError) {
    return <ErrorText error={tenant.error} />;
  }
  const items = [...coreNav, ...registeredApp.tenantNav].filter((i) =>
    can(tenant.data, i.permission),
  );
  const link = buttonVariants({ variant: "ghost", size: "sm" });
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-2 border-b pb-3">
        <span className="mr-2 font-heading font-semibold">{tenant.data.name}</span>
        <nav aria-label={texts.shell.tenantNav} className="flex flex-wrap gap-1">
          <Link
            to="/t/$tenantId"
            params={{ tenantId }}
            activeOptions={{ exact: true }}
            className={link}
            activeProps={{ "aria-current": "page", className: "bg-muted" }}
          >
            {texts.shell.overview}
          </Link>
          {items.map((i) => (
            <Link
              key={i.to}
              to={i.to}
              params={{ tenantId }}
              className={link}
              activeProps={{ "aria-current": "page", className: "bg-muted" }}
            >
              {i.label}
            </Link>
          ))}
        </nav>
      </div>
      <Outlet />
    </div>
  );
}
