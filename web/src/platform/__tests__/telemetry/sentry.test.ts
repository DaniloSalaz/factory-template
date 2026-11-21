import * as Sentry from "@sentry/react";
import { describe, expect, it } from "vitest";
import {
  initSentry,
  redactText,
  scrubBreadcrumb,
  scrubEvent,
  stripUrl,
} from "@/platform/telemetry/sentry";

// Values ADR 0004 forbids; none may leave the browser.
const forbidden = [
  "session=abc123cookie",
  "Bearer eyJhbGciOi",
  "code=oauth-code-123",
  "token=invite-token-456",
  "ana@example.com",
  "0190a1b2-0000-7000-8000-00000000aaaa",
];

function expectClean(value: unknown) {
  const text = JSON.stringify(value);
  for (const f of forbidden) {
    expect(text).not.toContain(f);
  }
}

describe("scrubEvent", () => {
  it("removes the user, headers, cookies, body, query string and fragment", () => {
    const event: Sentry.ErrorEvent = {
      type: undefined,
      message: "failed for ana@example.com at /auth/callback?code=oauth-code-123",
      user: {
        id: "0190a1b2-0000-7000-8000-00000000aaaa",
        email: "ana@example.com",
        ip_address: "{{auto}}",
      },
      request: {
        url: "https://app.example.com/invite?x=1#token=invite-token-456",
        headers: { Cookie: "session=abc123cookie", Authorization: "Bearer eyJhbGciOi" },
        cookies: { session: "abc123cookie" },
        query_string: "code=oauth-code-123",
        data: '{"email":"ana@example.com"}',
      },
      transaction: "/invite#token=invite-token-456",
      exception: { values: [{ type: "Error", value: "open /invite#token=invite-token-456" }] },
      tags: { user_id: "0190a1b2-0000-7000-8000-00000000aaaa", route: "/t/$tenantId/members" },
      extra: { email: "ana@example.com" },
      breadcrumbs: [{ message: "GET /auth/callback?code=oauth-code-123" }],
      contexts: { user: { email: "ana@example.com" }, browser: { name: "Chrome" } },
    };
    const out = scrubEvent(event);
    expectClean(out);
    expect(out.user).toBeUndefined();
    expect(out.request).toEqual({ url: "https://app.example.com/invite" });
    expect(out.tags).toEqual({ route: "/t/$tenantId/members" });
    expect(out.contexts).toEqual({ browser: { name: "Chrome" } });
    expect(out.message).toContain("[email]");
  });
});

describe("scrubBreadcrumb", () => {
  it("keeps URLs without query or fragment and drops other data", () => {
    const out = scrubBreadcrumb({
      category: "navigation",
      data: { from: "/invite#token=invite-token-456", to: "/login?return_to=%2Finvite", body: "x" },
      message: "to ana@example.com",
    });
    expectClean(out);
    expect(out?.data).toEqual({ from: "/invite", to: "/login" });
  });
});

describe("helpers", () => {
  it("strips URLs and redacts text", () => {
    expect(stripUrl("/a?b=1#c")).toBe("/a");
    expect(redactText("password=hunter2 for ana@example.com")).toBe(
      "password=[REDACTED] for [email]",
    );
  });

  it("does not start Sentry without a DSN", () => {
    expect(initSentry({ dsn: undefined, release: "dev", environment: "test" })).toBe(false);
    expect(initSentry({ dsn: "", release: "dev", environment: "test" })).toBe(false);
    expect(Sentry.getClient()).toBeUndefined();
  });
});
