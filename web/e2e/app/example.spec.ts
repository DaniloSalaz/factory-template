// The app's own journey (e2e/app/), against the real local API like the core's in
// e2e/: its screen, registered in src/app/registry.ts, inside the tenant layout.
import { cleanup, newTenant, newUser, run, type User } from "../db.ts";
import { expect, shot, signIn, test } from "../fixtures.ts";

let bea: User;
let tenant: string;

test.beforeAll(() => {
  bea = newUser("bea");
  // admin requires no MFA in the example catalog and has tenant:update.
  tenant = newTenant("ejemplo", [{ user: bea, role: "admin" }]);
});

test.afterAll(() => cleanup());

test("the example screen opens from the tenant's navigation", async ({ page, context }) => {
  await signIn(context, bea);
  await page.goto(`/t/${tenant}`);
  await expect(page.getByRole("heading", { name: `ejemplo ${run}` })).toBeVisible();

  await page.getByRole("link", { name: "Ejemplo" }).click();
  await expect(page).toHaveURL(new RegExp(`/t/${tenant}/example$`));
  await expect(page.getByRole("heading", { name: "Pantalla de ejemplo" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Renombrar la organización" })).toBeVisible();
  await shot(page, "app-01-example");
});
