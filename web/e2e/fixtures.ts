import { type BrowserContext, test as base, type Page } from "@playwright/test";
import type { User } from "./db.ts";

/** signIn puts the seeded session's cookie in the browser, as /auth/callback would. */
export async function signIn(context: BrowserContext, user: User): Promise<void> {
  await context.addCookies([
    {
      name: "__Host-session",
      value: user.token,
      domain: "localhost",
      path: "/",
      secure: true,
      httpOnly: true,
      sameSite: "Lax",
    },
  ]);
}

/** shot saves a full-page capture for the PR (e2e-screenshots/, not committed). */
export async function shot(page: Page, name: string): Promise<void> {
  await page.screenshot({ path: `e2e-screenshots/${name}.png`, fullPage: true });
}

export const test = base;
export { expect } from "@playwright/test";
