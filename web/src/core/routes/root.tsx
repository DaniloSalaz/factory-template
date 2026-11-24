import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";
import type { PlatformContext } from "@/core/bootstrap";

export const Route = createRootRouteWithContext<PlatformContext>()({
  component: () => <Outlet />,
});
