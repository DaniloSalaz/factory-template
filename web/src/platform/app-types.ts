import type { LinkProps } from "@tanstack/react-router";
import type { ComponentType } from "react";

// The routes below /t/$tenantId, from the route tree: a typo in the app's registry fails
// the type check.
type TenantRoute = Extract<NonNullable<LinkProps["to"]>, `/t/$tenantId/${string}`>;

/** An entry of the tenant's navigation, registered by the app (src/app/registry.ts). */
export type TenantNavItem = {
  label: string;
  /** A route of src/app/routes, e.g. "/t/$tenantId/example". */
  to: TenantRoute;
  /** Shown only if the role has this permission (UX only: the API decides). */
  permission?: string;
};

/** What an app registers in the platform: its name, its logo and its navigation. */
export type AppRegistration = {
  name: string;
  Logo: ComponentType<{ className?: string }>;
  tenantNav: readonly TenantNavItem[];
};
