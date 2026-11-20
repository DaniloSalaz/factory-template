import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { MfaEnroll } from "@/platform/components/MfaEnroll";
import { Page } from "@/platform/components/Page";
import { paths, safeReturnTo } from "@/platform/paths";
import { texts } from "@/platform/texts";
import { Card, CardContent } from "@/platform/ui/card";

// Where 403 auth.mfa_enrollment_required sends; back to return_to once the factor works.
export const Route = createFileRoute("/_shell/account/mfa/enroll")({
  validateSearch: (search: Record<string, unknown>) => ({
    return_to: safeReturnTo(search["return_to"]),
  }),
  component: Enroll,
});

function Enroll() {
  const { return_to } = Route.useSearch();
  const navigate = useNavigate();
  return (
    <Page
      title={texts.mfa.enrollTitle}
      description={return_to === paths.mfa ? texts.mfa.description : texts.mfa.enrollRequired}
    >
      <Card>
        <CardContent>
          <MfaEnroll onEnrolled={() => void navigate({ to: return_to, replace: true })} />
        </CardContent>
      </Card>
    </Page>
  );
}
