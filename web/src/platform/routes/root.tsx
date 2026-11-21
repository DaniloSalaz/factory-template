import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";
import type { PlatformContext } from "@/platform/bootstrap";

export const Route = createRootRouteWithContext<PlatformContext>()({
  component: () => <Outlet />,
});
