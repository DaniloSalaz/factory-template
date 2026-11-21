// Against the real local API, with sessions seeded by SQL (docs/platform/runbooks/auth.md).
import { cleanup, newTenant, newUser, run, type User } from "./db.ts";
import { expect, shot, signIn, test } from "./fixtures.ts";

let ana: User;
let otra: User;
let mine: string;
let foreign: string;

test.beforeAll(() => {
  ana = newUser("ana");
  otra = newUser("otra");
  // admin requires no MFA in the example catalog: these screens test tenancy, not MFA.
  mine = newTenant("despacho", [{ user: ana, role: "admin" }]);
  newTenant("archivado", [{ user: ana, role: "member" }], "suspended");
  newTenant("ocupado", [{ user: otra, role: "owner" }]);
  foreign = newTenant("ajeno", [{ user: otra, role: "owner" }]);
});

test.afterAll(() => cleanup());

test("without a session, any screen sends to /login and back", async ({ page }) => {
  await page.goto("/tenants");
  await expect(page).toHaveURL(/\/login\?return_to=%2Ftenants$/);
  await expect(page.getByRole("button", { name: "Entrar" })).toBeVisible();
  await shot(page, "01-login");
});

test("the selector lists the tenants of GET /me, suspended ones disabled", async ({
  page,
  context,
}) => {
  await signIn(context, ana);
  await page.goto("/");
  // No last tenant yet: / goes to the selector.
  await expect(page).toHaveURL(/\/tenants$/);
  const list = page.getByRole("list", { name: "Organizaciones" });
  await expect(list.getByRole("link", { name: new RegExp(`despacho ${run}`) })).toBeVisible();
  await expect(list.getByText(`archivado ${run}`)).toBeVisible();
  await expect(list.getByRole("link", { name: new RegExp(`archivado ${run}`) })).toHaveCount(0);
  await expect(list.getByText("Suspendida")).toBeVisible();
  await shot(page, "02-selector");
});

test("the protected screen shows GET /tenant, and / returns to it", async ({ page, context }) => {
  await signIn(context, ana);
  await page.goto("/tenants");
  await page.getByRole("link", { name: new RegExp(`despacho ${run}`) }).click();
  await expect(page).toHaveURL(new RegExp(`/t/${mine}$`));
  await expect(page.getByRole("heading", { name: `despacho ${run}` })).toBeVisible();
  await expect(page.getByText("admin", { exact: true })).toBeVisible();
  const perms = page.getByRole("list", { name: "Permisos" });
  await expect(perms.getByText("members:manage")).toBeVisible();
  await expect(perms.getByText("tenant:own")).toHaveCount(0);
  await shot(page, "03-tenant");

  // The app's screen, registered in src/app/registry.ts, inside the tenant layout.
  await page.getByRole("link", { name: "Ejemplo" }).click();
  await expect(page).toHaveURL(new RegExp(`/t/${mine}/example$`));
  await expect(page.getByRole("heading", { name: "Pantalla de ejemplo" })).toBeVisible();
  await shot(page, "04-app-example");

  await page.goto("/");
  await expect(page).toHaveURL(new RegExp(`/t/${mine}$`));
});

test("a foreign X-Tenant-ID in the URL goes to the selector with a notice", async ({
  page,
  context,
}) => {
  await signIn(context, ana);
  await page.goto("/login");
  // As if it were the last tenant used: its 403 also forgets it.
  await page.evaluate((id) => localStorage.setItem("platform.lastTenantId", id), foreign);
  await page.goto(`/t/${foreign}`);
  await expect(page).toHaveURL(/\/tenants\?notice=tenant_forbidden$/);
  await expect(page.getByRole("alert")).toContainText("No tienes acceso a esa organización");
  expect(await page.evaluate(() => localStorage.getItem("platform.lastTenantId"))).toBeNull();
  await shot(page, "05-tenant-forbidden");
});

test("a last tenant that GET /me no longer lists is forgotten", async ({ page, context }) => {
  await signIn(context, ana);
  await page.goto("/login");
  await page.evaluate((id) => localStorage.setItem("platform.lastTenantId", id), foreign);
  await page.goto("/");
  await expect(page).toHaveURL(/\/tenants$/);
  expect(await page.evaluate(() => localStorage.getItem("platform.lastTenantId"))).toBeNull();
});

test("create a tenant: slug errors by code, then the owner's MFA enrollment", async ({
  page,
  context,
}) => {
  await signIn(context, ana);
  await page.goto("/tenants");
  const name = page.getByLabel("Nombre");
  const slug = page.getByLabel("Identificador");
  const create = page.getByRole("button", { name: "Crear" });

  await name.fill(`Nuevo ${run}`);
  await slug.fill("Con Mayúsculas");
  await create.click();
  await expect(page.getByText("El identificador debe tener de 3 a 40")).toBeVisible();

  await slug.fill("admin");
  await create.click();
  await expect(page.getByText("Ese identificador está reservado")).toBeVisible();

  await slug.fill(`ocupado-${run}`);
  await create.click();
  await expect(page.getByText("Ese identificador ya está en uso")).toBeVisible();
  await shot(page, "06-create-tenant-errors");

  await slug.fill(`nuevo-${run}`);
  await create.click();
  // The creator is owner, whose role requires MFA: the first GET /tenant answers 403
  // auth.mfa_enrollment_required and the error handler sends to the enrollment.
  await expect(page).toHaveURL(/\/account\/mfa\/enroll\?return_to=%2Ft%2F[0-9a-f-]+$/);
  await expect(page.getByText("Tu rol en esta organización exige")).toBeVisible();
  await shot(page, "07-mfa-enroll-required");
});

test("logout revokes the session and goes to /login", async ({ page, context }) => {
  const eva = newUser("eva");
  await signIn(context, eva);
  await page.goto("/tenants");
  await expect(page.getByText(eva.email)).toBeVisible();
  await page.getByRole("button", { name: "Salir", exact: true }).click();
  await expect(page).toHaveURL(/\/login/);
  await page.goto("/tenants");
  await expect(page).toHaveURL(/\/login\?return_to=%2Ftenants$/);
});
