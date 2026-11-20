import { createRouter } from "@tanstack/react-router";
import { createPlatform } from "@/platform/bootstrap";
import { routeTree } from "./routeTree.gen";

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
