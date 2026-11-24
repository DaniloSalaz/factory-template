import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useId, useState } from "react";
import { getGetCurrentTenantQueryKey, getGetMeQueryKey, useUpdateCurrentTenant } from "@/gen/api";
import { ErrorText } from "@/platform/components/ErrorText";
import { Page } from "@/platform/components/Page";
import { errorCode, errorMessage } from "@/platform/errors/message";
import { can, useCurrentTenant } from "@/platform/tenant/use-tenant";
import { texts } from "@/platform/texts";
import { Badge } from "@/platform/ui/badge";
import { Button } from "@/platform/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/platform/ui/card";
import { Input } from "@/platform/ui/input";
import { Label } from "@/platform/ui/label";

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
      {can(t, "tenant:update") && <RenameTenant current={t.name} />}
    </Page>
  );
}

// PATCH /tenant (tenant:update). The name is in GET /tenant and in GET /me (the selector).
function RenameTenant(props: { current: string }) {
  const nameId = useId();
  const [name, setName] = useState(props.current);
  const queryClient = useQueryClient();
  const rename = useUpdateCurrentTenant({
    mutation: {
      onSuccess: async () => {
        await queryClient.invalidateQueries({ queryKey: getGetCurrentTenantQueryKey() });
        await queryClient.invalidateQueries({ queryKey: getGetMeQueryKey() });
      },
    },
  });
  const nameError =
    errorCode(rename.error) === "tenant.name_invalid" ? errorMessage(rename.error) : null;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{texts.tenant.renameTitle}</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            rename.mutate({ data: { name } });
          }}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={nameId}>{texts.tenant.name}</Label>
            <Input
              id={nameId}
              value={name}
              onChange={(e) => {
                rename.reset();
                setName(e.target.value);
              }}
              aria-invalid={nameError ? true : undefined}
              aria-describedby={nameError ? `${nameId}-error` : undefined}
              required
            />
            {nameError && (
              <p id={`${nameId}-error`} className="text-sm text-destructive">
                {nameError}
              </p>
            )}
          </div>
          {!nameError && <ErrorText error={rename.error} />}
          {rename.isSuccess && (
            <p role="status" className="text-sm text-muted-foreground">
              {texts.tenant.renamed}
            </p>
          )}
          <div>
            <Button type="submit" disabled={rename.isPending || name.trim() === props.current}>
              {rename.isPending ? texts.tenant.renaming : texts.tenant.rename}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
