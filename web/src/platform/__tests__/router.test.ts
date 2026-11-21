import { afterEach, describe, expect, it, vi } from "vitest";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.resetModules();
  sessionStorage.clear();
});

// The start-up itself (src/router.ts): the token leaves the URL before the router reads
// it, so the router's location, its history and any return_to never hold it.
describe("start-up on an invitation link", () => {
  it("removes the fragment before the router exists", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>();
    vi.stubGlobal("fetch", fetch);
    window.history.replaceState(null, "", "/invite#token=secret-token");

    const { router } = await import("@/router");

    expect(window.location.href).not.toContain("secret-token");
    expect(window.location.hash).toBe("");
    expect(router.state.location.href).toBe("/invite");
    expect(router.history.location.href).not.toContain("secret-token");
    expect(sessionStorage.getItem("platform.inviteToken")).toBe("secret-token");
    expect(fetch).not.toHaveBeenCalled();
  });
});
