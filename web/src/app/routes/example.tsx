import { createFileRoute } from "@tanstack/react-router";
import { Page } from "@/platform/components/Page";
import { can, useCurrentTenant } from "@/platform/tenant/use-tenant";
import { Button } from "@/platform/ui/button";
import { Card, CardContent } from "@/platform/ui/card";
import { appTexts } from "../texts";

// An app screen: a file of src/app/routes, mounted inside /t/$tenantId (its URL is
// /t/$tenantId/example). Its entry in the navigation is in src/app/registry.ts.
export const Route = createFileRoute("/_shell/t/$tenantId/example")({
  component: Example,
});

function Example() {
  const tenant = useCurrentTenant();
  if (!tenant.isSuccess) {
    return null;
  }
  return (
    <Page title={appTexts.exampleTitle} description={appTexts.exampleDescription(tenant.data.name)}>
      {can(tenant.data, "tenant:update") && (
        <Card>
          <CardContent className="flex flex-col gap-2">
            <div>
              <Button variant="outline">{appTexts.exampleAction}</Button>
            </div>
            <p className="text-sm text-muted-foreground">{appTexts.exampleActionNote}</p>
          </CardContent>
        </Card>
      )}
    </Page>
  );
}
