// Members and invitations (design 5.6 and 5.7) against the real API, with sessions seeded
// by SQL. owner requires MFA in the example catalog, so owners get a session with MFA.
import {
  cleanup,
  emailOf,
  knownInviteToken,
  newSession,
  newTenant,
  newUser,
  run,
  type User,
} from "./db.ts";
import { expect, shot, signIn, test } from "./fixtures.ts";

let owner: User;
let admin: User;
let member: User;
let tenant: string;

test.describe.configure({ mode: "serial" });

test.beforeAll(() => {
  owner = newUser("duena", { mfa: true });
  admin = newUser("gestora");
  member = newUser("socio");
  tenant = newTenant("equipo", [
    { user: owner, role: "owner" },
    { user: admin, role: "admin" },
    { user: member, role: "member" },
  ]);
});

test.afterAll(() => cleanup());

const invited = emailOf("invitada");

test("the owner invites an admin; the invitee accepts from /invite with no session yet", async ({
  page,
  context,
  browser,
}) => {
  await signIn(context, owner);
  await page.goto(`/t/${tenant}`);
  const nav = page.getByRole("navigation", { name: "Navegación de la organización" });
  await expect(nav.getByRole("link", { name: "Miembros" })).toBeVisible();
  await nav.getByRole("link", { name: "Invitaciones" }).click();
  await expect(page).toHaveURL(new RegExp(`/t/${tenant}/invitations$`));

  await page.getByLabel("Email").fill(invited);
  await page.getByLabel("Rol", { exact: true }).selectOption("admin");
  await page.getByRole("button", { name: "Invitar" }).click();
  await expect(page.getByRole("status")).toContainText(`Invitación enviada a ${invited}`);
  const list = page.getByRole("list", { name: "Invitaciones" });
  await expect(list.getByText(invited)).toBeVisible();
  await expect(list.getByText("Rol: admin")).toBeVisible();

  // Inviting the same email again: invitation.pending, with the pending one to resend.
  await page.getByLabel("Email").fill(invited);
  await page.getByRole("button", { name: "Invitar" }).click();
  await expect(page.getByRole("alert")).toContainText("ya tiene una invitación pendiente");
  await page.getByRole("button", { name: "Reenviar la pendiente" }).click();
  await expect(page.getByRole("status")).toContainText("reenviada con un enlace nuevo");
  await shot(page, "20-invitations");

  // The invitee opens the link in a browser of their own, without a session.
  const invitee = newUser("invitada", { session: false });
  const token = knownInviteToken(tenant, invitee.email);
  const theirs = await browser.newContext({ baseURL: "http://localhost:5173", locale: "es-ES" });
  const tab = await theirs.newPage();
  await tab.goto(`/invite#token=${token}`);
  await expect(tab).toHaveURL(/\/login\?return_to=%2Finvite$/);
  expect(tab.url()).not.toContain(token);
  expect(await tab.evaluate(() => sessionStorage.getItem("platform.inviteToken"))).toBe(token);
  await shot(tab, "21-invite-login");

  // AuthKit's callback would bring them back to return_to with a session.
  newSession(invitee);
  await signIn(theirs, invitee);
  await tab.goto("/invite");
  await expect(tab).toHaveURL(new RegExp(`/t/${tenant}$`));
  await expect(tab.getByText("admin", { exact: true })).toBeVisible();
  expect(await tab.evaluate(() => sessionStorage.getItem("platform.inviteToken"))).toBeNull();
  await shot(tab, "22-invite-accepted");
  await theirs.close();

  await page.reload();
  await expect(page.getByRole("list", { name: "Invitaciones" })).toHaveCount(0);
});

test("the owner changes the invitee's role and removes them", async ({ page, context }) => {
  await signIn(context, owner);
  await page.goto(`/t/${tenant}/members`);
  const list = page.getByRole("list", { name: "Miembros" });
  const row = list.getByRole("listitem").filter({ hasText: invited });
  await expect(row).toContainText("admin");

  await row.getByLabel("Rol de invitada").selectOption("member");
  await row.getByRole("button", { name: "Cambiar rol" }).click();
  await expect(row).toContainText(`${invited} · member`);
  await shot(page, "23-members");

  await row.getByRole("button", { name: "Expulsar" }).click();
  await expect(row).toContainText("¿Expulsar a invitada");
  await row.getByRole("button", { name: "Sí, expulsar" }).click();
  await expect(list.getByText(invited)).toHaveCount(0);
});

test("the last owner cannot leave the tenant", async ({ page, context }) => {
  await signIn(context, owner);
  await page.goto(`/t/${tenant}/members`);
  const mine = page.getByRole("listitem").filter({ hasText: "Tú" });
  await mine.getByRole("button", { name: "Salir de la organización" }).click();
  await mine.getByRole("button", { name: "Sí, salir" }).click();
  await expect(mine.getByRole("alert")).toContainText("se quedaría sin propietario");
  await expect(page).toHaveURL(new RegExp(`/t/${tenant}/members$`));
  await shot(page, "24-last-owner");
});

test("an admin is offered no owner role, and cannot touch the owner", async ({ page, context }) => {
  await signIn(context, admin);
  await page.goto(`/t/${tenant}/invitations`);
  await expect(page.getByLabel("Rol", { exact: true }).locator("option")).toHaveText([
    "admin",
    "member",
  ]);

  await page.goto(`/t/${tenant}/members`);
  const socio = page.getByRole("listitem").filter({ hasText: member.email });
  await expect(socio.getByLabel("Rol de socio").locator("option")).toHaveText(["admin", "member"]);

  // The owner's row offers controls (hiding is only UX), and the API refuses.
  const duena = page.getByRole("listitem").filter({ hasText: owner.email });
  await duena.getByRole("button", { name: "Expulsar" }).click();
  await duena.getByRole("button", { name: "Sí, expulsar" }).click();
  await expect(duena.getByRole("alert")).toContainText("rol por debajo del tuyo");
  await shot(page, "25-admin-members");
});

test("a member sees the members but no invitations", async ({ page, context }) => {
  await signIn(context, member);
  await page.goto(`/t/${tenant}`);
  const nav = page.getByRole("navigation", { name: "Navegación de la organización" });
  await expect(nav.getByRole("link", { name: "Miembros" })).toBeVisible();
  await expect(nav.getByRole("link", { name: "Invitaciones" })).toHaveCount(0);
  await nav.getByRole("link", { name: "Miembros" }).click();
  await expect(page.getByRole("button", { name: "Expulsar" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Cambiar rol" })).toHaveCount(0);
});

test("an invitation opened with another account says which email to use", async ({
  page,
  context,
}) => {
  await signIn(context, owner);
  await page.goto(`/t/${tenant}/invitations`);
  const target = emailOf("destinataria");
  await page.getByLabel("Email").fill(target);
  await page.getByRole("button", { name: "Invitar" }).click();
  await expect(page.getByRole("status")).toContainText(target);

  const other = newUser("otracuenta");
  const token = knownInviteToken(tenant, target);
  await context.clearCookies();
  await signIn(context, other);
  await page.goto(`/invite#token=${token}`);
  await expect(page.getByRole("alert")).toContainText(
    "La invitación es para otro email. Sal, entra con la cuenta del email invitado",
  );
  expect(page.url()).not.toContain(token);
  expect(await page.evaluate(() => sessionStorage.getItem("platform.inviteToken"))).toBeNull();
  await expect(
    page.getByRole("button", { name: "Salir para entrar con otra cuenta" }),
  ).toBeVisible();
  await shot(page, "26-invite-email-mismatch");
});

test("the owner renames the tenant", async ({ page, context }) => {
  await signIn(context, owner);
  await page.goto(`/t/${tenant}`);
  const name = page.getByLabel("Nombre");
  await name.fill(`Equipo renombrado ${run}`);
  await page.getByRole("button", { name: "Guardar" }).click();
  await expect(page.getByRole("heading", { name: `Equipo renombrado ${run}` })).toBeVisible();
  await expect(page.getByRole("status")).toContainText("Nombre cambiado");
});

test("a 429 shows the wait of Retry-After", async ({ page, context }) => {
  const prisa = newUser("prisa");
  // POST /tenants allows 5 an hour per user: spend them, then the screen gets the 429.
  for (let i = 1; i <= 5; i++) {
    const res = await page.request.post("/api/tenants", {
      headers: { Cookie: `__Host-session=${prisa.token}`, Origin: "http://localhost:5173" },
      data: { name: `Prisa ${i} ${run}`, slug: `prisa${i}-${run}` },
    });
    expect(res.status()).toBe(201);
  }
  await signIn(context, prisa);
  await page.goto("/tenants");
  await page.getByLabel("Nombre").fill(`Prisa 6 ${run}`);
  await page.getByLabel("Identificador").fill(`prisa6-${run}`);
  await page.getByRole("button", { name: "Crear" }).click();
  await expect(page.getByRole("status")).toContainText(
    /Demasiados intentos\. Vuelve a intentarlo dentro de \d+ minutos\./,
  );
  await shot(page, "27-rate-limited");
});
