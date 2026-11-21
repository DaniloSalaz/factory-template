import { createFileRoute } from "@tanstack/react-router";
import { ExamplePage } from "@/app/features/example/pages/ExamplePage";

// An app screen: a file of src/app/routes, mounted inside /t/$tenantId (its URL is
// /t/$tenantId/example), that only maps the URL to its page in src/app/features. Its
// entry in the navigation is in src/app/registry.ts.
export const Route = createFileRoute("/_shell/t/$tenantId/example")({
  component: ExamplePage,
});
