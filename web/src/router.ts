import { createRouter } from "@tanstack/react-router";
import { createPlatform } from "@/platform/bootstrap";
import { captureInviteToken } from "@/platform/invite/invite-token";
import { routeTree } from "./routeTree.gen";

// Before anything reads the URL: an invitation's token leaves the fragment (design 5.6.3).
captureInviteToken(window);

// Composition root of the front end: the platform's services, the route tree (platform
// and app routes, src/platform/routes.ts) and the router that connects them.
export const platform = createPlatform();

export const router = createRouter({
  routeTree,
  context: platform.context,
  defaultPreload: "intent",
  scrollRestoration: true,
});

platform.attach(router);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
