import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";
import type { CoreContext } from "@/core/bootstrap";

export const Route = createRootRouteWithContext<CoreContext>()({
  component: () => <Outlet />,
});
