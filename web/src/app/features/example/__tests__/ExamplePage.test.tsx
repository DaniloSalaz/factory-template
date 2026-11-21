import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ExamplePage } from "@/app/features/example/pages/ExamplePage";
import { appTexts } from "@/app/texts";
import { type CurrentTenant, getGetCurrentTenantQueryKey } from "@/gen/api";

// The tenant layout has already loaded GET /tenant when the page renders, so the test
// puts it in the cache instead of answering a request.
function renderWith(permissions: string[]) {
  const tenant: CurrentTenant = {
    id: "0190a1b2-0000-7000-8000-0000000000a1",
    slug: "despacho",
    name: "Despacho",
    role: "admin",
    scope: "tenant",
    permissions,
  };
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  client.setQueryData(getGetCurrentTenantQueryKey(), tenant);
  render(
    <QueryClientProvider client={client}>
      <ExamplePage />
    </QueryClientProvider>,
  );
}

describe("ExamplePage", () => {
  it("shows the action to a role with tenant:update", () => {
    renderWith(["members:read", "tenant:update"]);
    expect(screen.getByRole("heading", { name: appTexts.exampleTitle })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: appTexts.exampleAction })).toBeInTheDocument();
  });

  it("hides it from a role without tenant:update", () => {
    renderWith(["members:read"]);
    expect(screen.getByRole("heading", { name: appTexts.exampleTitle })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: appTexts.exampleAction })).toBeNull();
  });
});
