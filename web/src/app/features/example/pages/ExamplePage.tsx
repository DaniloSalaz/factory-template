import { appTexts } from "@/app/texts";
import { Page } from "@/platform/components/Page";
import { can, useCurrentTenant } from "@/platform/tenant/use-tenant";
import { Button } from "@/platform/ui/button";
import { Card, CardContent } from "@/platform/ui/card";

export function ExamplePage() {
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
