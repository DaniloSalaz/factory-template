// The MFA flows with simulated API responses (page.route): there is no fake of WorkOS in
// the backend, and a real TOTP code cannot be produced here. Everything else (routing,
// the error handler, the screens) is the real front end.
import type { Page, Route } from "@playwright/test";
import { expect, shot, test } from "./fixtures.ts";

const tenantId = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b";
const factorId = "0190a1b2-0000-7000-8000-0000000000f1";
const secret = "JBSWY3DPEHPK3PXP";
// A stand-in for the PNG the API sends; any image data URI renders the same way.
const qr = `data:image/svg+xml;base64,${Buffer.from(
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8" shape-rendering="crispEdges"><path d="M0 0h3v3H0zM5 0h3v3H5zM0 5h3v3H0zM4 4h1v1H4zM6 5h1v2H6zM4 6h1v2H4z"/></svg>',
).toString("base64")}`;

const problem = (status: number, code: string, headers: Record<string, string> = {}) => ({
  status,
  contentType: "application/problem+json",
  headers,
  body: JSON.stringify({ type: "about:blank", title: code, status, code, request_id: "req-e2e" }),
});
const ok = (status: number, body: unknown) => ({
  status,
  contentType: "application/json",
  body: JSON.stringify(body),
});

type State = { factors: { verified: boolean }[]; sessionMfa: boolean };

function me(s: State) {
  return {
    user: { id: "u1", email: "ana@example.test", display_name: "Ana" },
    memberships: [
      {
        tenant_id: tenantId,
        tenant_slug: "despacho",
        tenant_name: "Despacho Ana",
        tenant_status: "active",
        role: "owner",
        mfa_required: true,
      },
    ],
    mfa_enrolled: s.factors.some((f) => f.verified),
    mfa_factors: s.factors.map((f) => ({
      id: factorId,
      type: "totp",
      verified: f.verified,
      created_at: "2026-11-01T10:00:00Z",
    })),
    session_mfa_at: s.sessionMfa ? "2026-11-20T09:30:00Z" : null,
  };
}

const tenant = {
  id: tenantId,
  slug: "despacho",
  name: "Despacho Ana",
  role: "owner",
  scope: "tenant",
  permissions: ["members:read", "scope:tenant", "tenant:own", "tenant:update"],
};

/** fakeApi answers /api/* like the API would, from a small state. */
async function fakeApi(page: Page, s: State, extra?: (route: Route, path: string) => boolean) {
  // Only the API: a glob like **/api/** would also catch Vite's /src/platform/api modules.
  await page.route(
    (url) => url.pathname.startsWith("/api/"),
    async (route) => {
      const req = route.request();
      const path = new URL(req.url()).pathname.replace(/^\/api/, "");
      if (extra?.(route, path)) return;
      const key = `${req.method()} ${path}`;
      if (key === "GET /me") return route.fulfill(ok(200, me(s)));
      if (key === "GET /tenant") {
        if (!s.sessionMfa) {
          const verified = s.factors.some((f) => f.verified);
          return route.fulfill(
            problem(403, verified ? "auth.mfa_required" : "auth.mfa_enrollment_required"),
          );
        }
        expect(req.headers()["x-tenant-id"]).toBe(tenantId);
        return route.fulfill(ok(200, tenant));
      }
      if (key === "POST /me/mfa/totp") {
        s.factors.push({ verified: false });
        return route.fulfill(
          ok(201, {
            factor_id: factorId,
            challenge_id: "c-enroll",
            expires_at: new Date(Date.now() + 300_000).toISOString(),
            qr_code: qr,
            secret,
            uri: `otpauth://totp/Factory:ana@example.test?secret=${secret}`,
          }),
        );
      }
      if (key === "POST /me/mfa/challenges") {
        return route.fulfill(
          ok(201, { challenge_id: `c-${Date.now()}`, factor_id: factorId, expires_at: "" }),
        );
      }
      if (key === "POST /me/mfa/totp/verify") {
        const body = req.postDataJSON() as { code: string };
        if (body.code !== "123456") return route.fulfill(problem(422, "mfa.code_invalid"));
        for (const f of s.factors) f.verified = true;
        s.sessionMfa = true;
        return route.fulfill(ok(200, { mfa_verified_at: new Date().toISOString() }));
      }
      return route.fulfill(problem(404, "route_not_found"));
    },
  );
}

test("403 auth.mfa_required: verify a code and go back to the route", async ({ page }) => {
  await fakeApi(page, { factors: [{ verified: true }], sessionMfa: false });
  await page.goto(`/t/${tenantId}`);
  await expect(page).toHaveURL(/\/account\/mfa\/verify\?return_to=%2Ft%2F/);
  await shot(page, "08-mfa-verify");

  const code = page.getByLabel("Código");
  await code.fill("000000");
  await page.getByRole("button", { name: "Verificar" }).click();
  await expect(page.getByRole("alert")).toContainText("El código no es correcto");
  await shot(page, "09-mfa-verify-error");

  await code.fill("123456");
  await page.getByRole("button", { name: "Verificar" }).click();
  await expect(page).toHaveURL(new RegExp(`/t/${tenantId}$`));
  await expect(page.getByRole("heading", { name: "Despacho Ana" })).toBeVisible();
});

test("403 auth.mfa_enrollment_required: enroll, see the secret once, go back", async ({ page }) => {
  await fakeApi(page, { factors: [], sessionMfa: false });
  await page.goto(`/t/${tenantId}`);
  await expect(page).toHaveURL(/\/account\/mfa\/enroll\?return_to=%2Ft%2F/);
  await page.getByRole("button", { name: "Empezar el alta" }).click();
  await expect(page.getByAltText("Código QR para la app de autenticación")).toBeVisible();
  await expect(page.getByTestId("secret")).toHaveText(secret);
  await shot(page, "10-mfa-enroll");

  await page.getByLabel("Código").fill("123456");
  await page.getByRole("button", { name: "Verificar" }).click();
  await expect(page).toHaveURL(new RegExp(`/t/${tenantId}$`));
  await expect(page.getByRole("heading", { name: "Despacho Ana" })).toBeVisible();

  // Neither the secret nor the URI is kept anywhere.
  const stored = await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }));
  expect(stored).not.toContain(secret);
  await expect(page.getByText(secret)).toHaveCount(0);
});

test("the MFA screen: factors, and removal errors by code", async ({ page }) => {
  await fakeApi(page, { factors: [{ verified: true }], sessionMfa: true }, (route, path) => {
    if (route.request().method() === "DELETE" && path === `/me/mfa/totp/${factorId}`) {
      void route.fulfill(problem(403, "mfa.last_factor_required"));
      return true;
    }
    return false;
  });
  await page.goto("/account/mfa");
  await expect(page.getByText("Verificado")).toBeVisible();
  await expect(page.getByText(/Esta sesión verificó un código/)).toBeVisible();
  await page.getByRole("button", { name: "Quitar" }).click();
  await page.getByRole("button", { name: "Sí, quitar" }).click();
  await expect(page.getByRole("alert")).toContainText("No puedes quitar tu último factor");
  await shot(page, "11-mfa-account");
});

test("removing a factor without a recent MFA verifies first and comes back", async ({ page }) => {
  await fakeApi(page, { factors: [{ verified: true }], sessionMfa: false }, (route, path) => {
    if (route.request().method() === "DELETE" && path === `/me/mfa/totp/${factorId}`) {
      void route.fulfill(problem(403, "auth.mfa_required"));
      return true;
    }
    return false;
  });
  await page.goto("/account/mfa");
  await page.getByRole("button", { name: "Quitar" }).click();
  await page.getByRole("button", { name: "Sí, quitar" }).click();
  await expect(page).toHaveURL(/\/account\/mfa\/verify\?return_to=%2Faccount%2Fmfa$/);
  await page.getByLabel("Código").fill("123456");
  await page.getByRole("button", { name: "Verificar" }).click();
  await expect(page).toHaveURL(/\/account\/mfa$/);
  await expect(page.getByText(/Esta sesión verificó un código/)).toBeVisible();
});

test("429 shows how long to wait", async ({ page }) => {
  await fakeApi(page, { factors: [{ verified: true }], sessionMfa: false }, (route, path) => {
    if (path === "/me/mfa/totp/verify") {
      void route.fulfill(problem(429, "mfa.rate_limited", { "Retry-After": "30" }));
      return true;
    }
    return false;
  });
  await page.goto("/account/mfa/verify?return_to=%2Faccount%2Fmfa");
  await page.getByLabel("Código").fill("123456");
  await page.getByRole("button", { name: "Verificar" }).click();
  await expect(page.getByRole("status")).toContainText("dentro de 30 segundos");
  await shot(page, "12-rate-limited");
});
