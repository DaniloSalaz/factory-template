import { createRouter } from "@tanstack/react-router";
import { createCore } from "@/core/bootstrap";
import { RouteErrorScreen } from "@/core/components/ErrorScreen";
import { captureInviteToken } from "@/core/invite/invite-token";
import { initSentry } from "@/core/telemetry/sentry";
import { routeTree } from "./gen/routeTree.gen";

// Before anything reads the URL: an invitation's token leaves the fragment (auth-y-tenancy §5.6.3).
captureInviteToken(window);

// Error reports, only with VITE_SENTRY_DSN; after the token's capture, so Sentry never
// sees the fragment (ADR 0004).
initSentry({
  dsn: import.meta.env.VITE_SENTRY_DSN,
  release: import.meta.env.VITE_RELEASE ?? "dev",
  environment: import.meta.env.MODE,
});

// Composition root of the front end: the core's services, the route tree (core
// and app routes, src/core/routes.ts) and the router that connects them.
export const core = createCore();

export const router = createRouter({
  routeTree,
  context: core.context,
  defaultPreload: "intent",
  defaultErrorComponent: RouteErrorScreen,
  scrollRestoration: true,
});

core.attach(router);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
