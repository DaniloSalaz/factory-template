import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { MfaVerify } from "@/platform/components/MfaVerify";
import { Page } from "@/platform/components/Page";
import { paths, safeReturnTo } from "@/platform/paths";
import { texts } from "@/platform/texts";
import { Card, CardContent } from "@/platform/ui/card";

// Where 403 auth.mfa_required sends (platform/errors/handle.ts); back to return_to after.
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
