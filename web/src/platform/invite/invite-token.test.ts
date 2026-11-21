import { describe, expect, it, vi } from "vitest";
import {
  captureInviteToken,
  hasInviteToken,
  inviteTokenKey,
  takeInviteToken,
} from "./invite-token";

function fakeWindow(url: string) {
  const u = new URL(url, "https://app.example.test");
  const location = { pathname: u.pathname, search: u.search, hash: u.hash };
  const replaceState = vi.fn((_state: unknown, _unused: string, to: string) => {
    const next = new URL(to, u);
    Object.assign(location, { pathname: next.pathname, search: next.search, hash: next.hash });
  });
  const sessionStorage = window.sessionStorage;
  sessionStorage.clear();
  return {
    win: { location, history: { state: null, replaceState }, sessionStorage } as unknown as Pick<
      Window,
      "location" | "history" | "sessionStorage"
    >,
    location,
    replaceState,
    storage: sessionStorage,
  };
}

describe("captureInviteToken", () => {
  it("moves the token to sessionStorage and removes the fragment without navigating", () => {
    const w = fakeWindow("/invite#token=abc_DEF-123");
    captureInviteToken(w.win);
    expect(w.replaceState).toHaveBeenCalledOnce();
    expect(w.replaceState.mock.calls[0]?.[2]).toBe("/invite");
    expect(w.location.hash).toBe("");
    expect(w.storage.getItem(inviteTokenKey)).toBe("abc_DEF-123");
  });

  it("keeps the query but never the fragment", () => {
    const w = fakeWindow("/invite?x=1#token=t");
    captureInviteToken(w.win);
    expect(w.replaceState.mock.calls[0]?.[2]).toBe("/invite?x=1");
  });

  it("removes any fragment of /invite, even one without a token", () => {
    const w = fakeWindow("/invite#other=1");
    captureInviteToken(w.win);
    expect(w.location.hash).toBe("");
    expect(hasInviteToken(w.storage)).toBe(false);
  });

  it("does nothing on other paths or without a fragment", () => {
    for (const url of ["/tenants#token=abc", "/invite"]) {
      const w = fakeWindow(url);
      captureInviteToken(w.win);
      expect(w.replaceState).not.toHaveBeenCalled();
      expect(hasInviteToken(w.storage)).toBe(false);
    }
  });
});

describe("takeInviteToken", () => {
  it("returns the token once", () => {
    sessionStorage.setItem(inviteTokenKey, "t");
    expect(takeInviteToken(sessionStorage)).toBe("t");
    expect(takeInviteToken(sessionStorage)).toBeUndefined();
    expect(sessionStorage.getItem(inviteTokenKey)).toBeNull();
  });
});
