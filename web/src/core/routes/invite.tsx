import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useCallback, useState } from "react";
import { registeredApp } from "@/core/app-registry";
import { useCore } from "@/core/bootstrap";
import { AcceptInvitation, type AcceptOutcome } from "@/core/components/AcceptInvitation";
import { texts } from "@/core/texts";
import { Button, buttonVariants } from "@/core/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/core/ui/card";
import { useAuthLogout } from "@/gen/api";

// The invitation link's page (auth-y-tenancy §5.6.3). Outside the shell, so nothing runs before
// it but the token's capture at start-up (src/router.ts). index.html sets
// Referrer-Policy: no-referrer for the whole app, and so for this page.
export const Route = createFileRoute("/invite")({
  component: Invite,
});

function Invite() {
  const { Logo, name } = registeredApp;
  const navigate = useNavigate();
  const [ended, setEnded] = useState<{ outcome: AcceptOutcome; code?: string } | null>(null);
  const onAccepted = useCallback(
    (tenantId: string) =>
      void navigate({ to: "/t/$tenantId", params: { tenantId }, replace: true }),
    [navigate],
  );
  const onOutcome = useCallback(
    (outcome: AcceptOutcome, code: string | undefined) =>
      setEnded(code === undefined ? { outcome } : { outcome, code }),
    [],
  );

  return (
    <main className="flex min-h-svh items-center justify-center p-4">
      <Card className="w-full max-w-md">
        <CardHeader>
          <div className="mb-2 flex items-center gap-2">
            <Logo className="size-6" />
            <span className="font-heading font-semibold">{name}</span>
          </div>
          <CardTitle>{texts.invite.title}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <AcceptInvitation
            storage={window.sessionStorage}
            onAccepted={onAccepted}
            onOutcome={onOutcome}
          />
          {ended && (
            <div className="flex flex-wrap gap-2">
              {ended.code === "invitation.email_mismatch" && <SwitchAccount />}
              <Link to="/tenants" className={buttonVariants({ variant: "outline" })}>
                {texts.invite.toTenants}
              </Link>
            </div>
          )}
        </CardContent>
      </Card>
    </main>
  );
}

// The invitation is for another email: sign out, so the next login can pick the account
// the invitation was sent to. The token is gone; the link opens it again.
function SwitchAccount() {
  const { queryClient } = useCore();
  const navigate = useNavigate();
  const logout = useAuthLogout();
  return (
    <Button
      disabled={logout.isPending}
      onClick={() =>
        logout.mutate(undefined, {
          onSuccess: (res) => {
            queryClient.clear();
            if (res.logout_url) {
              window.location.assign(res.logout_url);
              return;
            }
            void navigate({ to: "/login", search: { return_to: "/" } });
          },
        })
      }
    >
      {texts.invite.logout}
    </Button>
  );
}
