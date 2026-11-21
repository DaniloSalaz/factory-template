import { defineConfig, devices } from "@playwright/test";

// The browser journey against the real local API (docs/runbooks/auth.md): Postgres up and
// migrated, and the API running (`mise run dev`). Playwright starts Vite if it is not
// running. Sessions are seeded with SQL, never through AuthKit; the MFA flows answer with
// simulated API responses (page.route), as there is no fake of WorkOS in the backend.
// The CI's e2e job runs it the same way and keeps every trace as an artifact.
const ci = Boolean(process.env["CI"]);
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  forbidOnly: true,
  reporter: ci ? [["list"], ["github"]] : [["list"]],
  globalSetup: "./e2e/global-setup.ts",
  use: {
    baseURL: "http://localhost:5173",
    locale: "es-ES",
    trace: ci ? "on" : "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "npm run dev -- --port 5173 --strictPort",
    url: "http://localhost:5173",
    reuseExistingServer: !ci,
  },
});
