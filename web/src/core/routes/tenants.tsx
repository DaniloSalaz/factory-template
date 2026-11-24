import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useId, useState } from "react";
import { getGetMeQueryKey, type Membership, useCreateTenant, useGetMe } from "@/gen/api";
import { usePlatform } from "@/platform/bootstrap";
import { ErrorText } from "@/platform/components/ErrorText";
import { Page } from "@/platform/components/Page";
import { errorCode, errorMessage } from "@/platform/errors/message";
import { texts } from "@/platform/texts";
import { Alert, AlertDescription } from "@/platform/ui/alert";
import { Badge } from "@/platform/ui/badge";
import { Button } from "@/platform/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/platform/ui/card";
import { Input } from "@/platform/ui/input";
import { Label } from "@/platform/ui/label";

export const Route = createFileRoute("/_shell/tenants")({
  validateSearch: (search: Record<string, unknown>): { notice?: "tenant_forbidden" } =>
    search["notice"] === "tenant_forbidden" ? { notice: "tenant_forbidden" } : {},
  component: Tenants,
});

function Tenants() {
  const { notice } = Route.useSearch();
  const me = useGetMe();
  return (
    <Page title={texts.tenants.title} description={texts.tenants.description}>
      {notice === "tenant_forbidden" && (
        <Alert role="alert">
          <AlertDescription>{texts.tenants.forbidden}</AlertDescription>
        </Alert>
      )}
      {me.isPending && <p>{texts.common.loading}</p>}
      {me.isError && <ErrorText error={me.error} />}
      {me.isSuccess && <TenantList memberships={me.data.memberships} />}
      <CreateTenant />
    </Page>
  );
}

function TenantList(props: { memberships: Membership[] }) {
  if (props.memberships.length === 0) {
    return <p className="text-muted-foreground">{texts.tenants.empty}</p>;
  }
  return (
    <ul className="flex flex-col gap-2" aria-label={texts.tenants.title}>
      {props.memberships.map((m) => {
        const suspended = m.tenant_status === "suspended";
        const body = (
          <>
            <span className="flex flex-col">
              <span className="font-medium">{m.tenant_name}</span>
              <span className="text-sm text-muted-foreground">
                {m.tenant_slug} · {texts.tenants.role(m.role)}
                {m.mfa_required && ` · ${texts.tenants.mfaRequired}`}
              </span>
            </span>
            {suspended && <Badge variant="secondary">{texts.tenants.suspended}</Badge>}
          </>
        );
        const cls = "flex items-center justify-between gap-4 rounded-lg border p-3";
        return (
          <li key={m.tenant_id}>
            {suspended ? (
              // Shown, but not a link: acting in it answers 403 auth.tenant_forbidden.
              <div className={`${cls} opacity-60`} aria-disabled="true">
                {body}
              </div>
            ) : (
              <Link
                to="/t/$tenantId"
                params={{ tenantId: m.tenant_id }}
                className={`${cls} hover:bg-muted`}
              >
                {body}
              </Link>
            )}
          </li>
        );
      })}
    </ul>
  );
}

const slugCodes = ["tenant.slug_invalid", "tenant.slug_reserved", "tenant.slug_taken"];

function CreateTenant() {
  const nameId = useId();
  const slugId = useId();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const { queryClient } = usePlatform();
  const navigate = useNavigate();
  const create = useCreateTenant({
    mutation: {
      onSuccess: async (t) => {
        await queryClient.invalidateQueries({ queryKey: getGetMeQueryKey() });
        await navigate({ to: "/t/$tenantId", params: { tenantId: t.id } });
      },
    },
  });

  const code = errorCode(create.error);
  const slugError = code && slugCodes.includes(code) ? errorMessage(create.error) : null;
  const nameError = code === "tenant.name_invalid" ? errorMessage(create.error) : null;
  const otherError = slugError || nameError ? null : create.error;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{texts.tenants.createTitle}</CardTitle>
        <CardDescription>{texts.tenants.createDescription}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate({ data: { name, slug } });
          }}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={nameId}>{texts.tenants.name}</Label>
            <Input
              id={nameId}
              value={name}
              onChange={(e) => setName(e.target.value)}
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
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={slugId}>{texts.tenants.slug}</Label>
            <Input
              id={slugId}
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              aria-invalid={slugError ? true : undefined}
              aria-describedby={`${slugId}-hint${slugError ? ` ${slugId}-error` : ""}`}
              autoCapitalize="none"
              spellCheck={false}
              required
            />
            <p id={`${slugId}-hint`} className="text-sm text-muted-foreground">
              {texts.tenants.slugHint}
            </p>
            {slugError && (
              <p id={`${slugId}-error`} className="text-sm text-destructive">
                {slugError}
              </p>
            )}
          </div>
          <ErrorText error={otherError} />
          <div>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? texts.tenants.creating : texts.tenants.create}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
