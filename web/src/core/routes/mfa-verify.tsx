import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { MfaVerify } from "@/core/components/MfaVerify";
import { Page } from "@/core/components/Page";
import { paths, safeReturnTo } from "@/core/paths";
import { texts } from "@/core/texts";
import { Card, CardContent } from "@/core/ui/card";

// Where 403 auth.mfa_required sends (core/errors/handle.ts); back to return_to after.
export const Route = createFileRoute("/_shell/account/mfa/verify")({
  validateSearch: (search: Record<string, unknown>) => ({
    return_to: safeReturnTo(search["return_to"]),
  }),
  component: Verify,
});

function Verify() {
  const { return_to } = Route.useSearch();
  const navigate = useNavigate();
  const fromMfaScreen = return_to === paths.mfa;
  return (
    <Page
      title={texts.mfa.verifyTitle}
      description={
        fromMfaScreen ? texts.mfa.verifyStandaloneDescription : texts.mfa.verifyDescription
      }
    >
      <Card>
        <CardContent>
          <MfaVerify onVerified={() => void navigate({ to: return_to, replace: true })} />
        </CardContent>
      </Card>
    </Page>
  );
}
