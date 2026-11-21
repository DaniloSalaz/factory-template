import type { AppRegistration } from "@/platform/app-types";
import { Logo } from "./brand/Logo";
import { appTexts } from "./texts";

// What this app adds to the platform's shell. Each app replaces it with its own.
export const app: AppRegistration = {
  name: "Factory Template",
  Logo,
  tenantNav: [{ label: appTexts.exampleNav, to: "/t/$tenantId/example" }],
};
