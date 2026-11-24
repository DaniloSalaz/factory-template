import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useGetMe } from "@/gen/api";
import { usePlatform } from "@/platform/bootstrap";
import { ErrorText } from "@/platform/components/ErrorText";
import { homeDestination } from "@/platform/tenant/last-tenant";
import { texts } from "@/platform/texts";

export const Route = createFileRoute("/_shell/")({
  component: Home,
});

// / goes to the last tenant used if GET /me still lists it as active, else to the picker.
function Home() {
  const me = useGetMe();
  const { lastTenant } = usePlatform();
  if (me.isPending) {
    return <p>{texts.common.loading}</p>;
  }
  if (me.isError) {
    return <ErrorText error={me.error} />;
  }
  const to = homeDestination(me.data, lastTenant);
  if (to.to === "/tenants") {
    return <Navigate to="/tenants" replace />;
  }
  return <Navigate to="/t/$tenantId" params={{ tenantId: to.tenantId }} replace />;
}
