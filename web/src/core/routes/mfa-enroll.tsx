import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { MfaEnroll } from "@/core/components/MfaEnroll";
import { Page } from "@/core/components/Page";
import { paths, safeReturnTo } from "@/core/paths";
import { texts } from "@/core/texts";
import { Card, CardContent } from "@/core/ui/card";

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
