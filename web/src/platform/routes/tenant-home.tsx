import { createFileRoute } from "@tanstack/react-router";
import { Page } from "@/platform/components/Page";
import { useCurrentTenant } from "@/platform/tenant/use-tenant";
import { texts } from "@/platform/texts";
import { Badge } from "@/platform/ui/badge";
import { Card, CardContent } from "@/platform/ui/card";

export const Route = createFileRoute("/_shell/t/$tenantId/")({
  component: TenantHome,
});

function TenantHome() {
  const tenant = useCurrentTenant();
  if (!tenant.isSuccess) {
    return null;
  }
  const t = tenant.data;
  return (
    <Page title={t.name} description={texts.tenant.permissionsNote}>
      <Card>
        <CardContent>
          <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-3 text-sm">
            <dt className="text-muted-foreground">{texts.tenant.slug}</dt>
            <dd>{t.slug}</dd>
            <dt className="text-muted-foreground">{texts.tenant.role}</dt>
            <dd>{t.role}</dd>
            <dt className="text-muted-foreground">{texts.tenant.scope}</dt>
            <dd>{texts.tenant.scopes[t.scope]}</dd>
            <dt className="text-muted-foreground">{texts.tenant.permissions}</dt>
            <dd>
              {t.permissions.length === 0 ? (
                texts.tenant.noPermissions
              ) : (
                <ul className="flex flex-wrap gap-1.5" aria-label={texts.tenant.permissions}>
                  {t.permissions.map((p) => (
                    <li key={p}>
                      <Badge variant="outline" className="font-mono">
                        {p}
                      </Badge>
                    </li>
                  ))}
                </ul>
              )}
            </dd>
          </dl>
        </CardContent>
      </Card>
    </Page>
  );
}
