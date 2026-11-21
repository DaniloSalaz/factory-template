import { type QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AnyRouter } from "@tanstack/react-router";
import { createContext, type ReactNode, useContext } from "react";
import { setTenantSource } from "@/platform/api/fetcher";
import { createQueryClient } from "@/platform/api/query-client";
import { createErrorHandler, type Destination } from "@/platform/errors/handle";
import { createNotices, type Notices } from "@/platform/errors/notices";
import { createLastTenantStore, type LastTenantStore } from "@/platform/tenant/last-tenant";

/** What every platform screen may use; also the router's context. */
export type PlatformContext = {
  queryClient: QueryClient;
  notices: Notices;
  lastTenant: LastTenantStore;
};

/** tenantOf reads $tenantId from the active route's matches; undefined outside /t/…. */
export function tenantOf(router: Pick<AnyRouter, "state">): string | undefined {
  const params: Record<string, unknown> = router.state.matches.at(-1)?.params ?? {};
  const id = params["tenantId"];
  return typeof id === "string" && id !== "" ? id : undefined;
}

/**
 * createPlatform builds the query client, the error handler and the notices, and
 * returns `attach` to connect them to the router once it exists (src/router.ts). The
 * tenant is always read from the active route, never stored.
 */
export function createPlatform(): { context: PlatformContext; attach: (r: AnyRouter) => void } {
  let router: AnyRouter | undefined;
  const routeTenant = () => (router ? tenantOf(router) : undefined);
  const notices = createNotices();
  const lastTenant = createLastTenantStore();
  const onError = createErrorHandler({
    location: () =>
      router?.state.location ?? {
        pathname: window.location.pathname,
        href: window.location.pathname,
      },
    navigate: (destination: Destination) => {
      void router?.navigate(destination);
    },
    routeTenant,
    lastTenant,
    notices,
  });
  const queryClient = createQueryClient({ onError, routeTenant });
  setTenantSource(routeTenant);
  return {
    context: { queryClient, notices, lastTenant },
    attach: (r) => {
      router = r;
    },
  };
}

const Context = createContext<PlatformContext | null>(null);

export function PlatformProvider(props: { value: PlatformContext; children: ReactNode }) {
  return (
    <Context value={props.value}>
      <QueryClientProvider client={props.value.queryClient}>{props.children}</QueryClientProvider>
    </Context>
  );
}

export function usePlatform(): PlatformContext {
  const ctx = useContext(Context);
  if (!ctx) {
    throw new Error("usePlatform outside PlatformProvider");
  }
  return ctx;
}
