import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Me } from "@/gen/api";
import { createQueryClient } from "@/platform/api/query-client";
import { actionFor } from "@/platform/errors/handle";
import { inviteTokenKey } from "@/platform/invite/invite-token";
import { texts } from "@/platform/texts";
import { AcceptInvitation } from "./AcceptInvitation";

const me: Me = {
  user: { id: "u", email: "ana@example.test", display_name: "Ana" },
  memberships: [],
  mfa_enrolled: false,
  mfa_factors: [],
  session_mfa_at: null,
};
const tenantId = "0190a1b2-0000-7000-8000-0000000000a1";

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status < 300 ? "application/json" : "application/problem+json" },
  });

type Accept = (body: unknown) => Response;

function setup(opts: { session: boolean; accept?: Accept }) {
  const accepts: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      if (url === "/api/me") {
        return opts.session
          ? json(200, me)
          : json(401, { status: 401, code: "auth.session_invalid" });
      }
      if (url === "/api/invitations/accept") {
        const body: unknown = JSON.parse(String(init?.body));
        accepts.push(body);
        return (opts.accept ?? (() => json(200, { tenant_id: tenantId, role: "admin" })))(body);
      }
      return json(404, {});
    }),
  );
  const onError = vi.fn();
  const onAccepted = vi.fn();
  const onOutcome = vi.fn();
  const client = createQueryClient({ onError, routeTenant: () => undefined });
  render(
    <StrictMode>
      <QueryClientProvider client={client}>
        <AcceptInvitation storage={sessionStorage} onAccepted={onAccepted} onOutcome={onOutcome} />
      </QueryClientProvider>
    </StrictMode>,
  );
  return { accepts, onError, onAccepted, onOutcome };
}

beforeEach(() => sessionStorage.setItem(inviteTokenKey, "the-token"));
afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

describe("AcceptInvitation", () => {
  it("without a session, the token waits and the handler goes to /login?return_to=/invite", async () => {
    const t = setup({ session: false });
    await vi.waitFor(() => expect(t.onError).toHaveBeenCalled());
    const action = actionFor(t.onError.mock.calls[0]?.[0], {
      pathname: "/invite",
      href: "/invite",
    });
    expect(action).toEqual({ kind: "login", returnTo: "/invite" });
    expect(JSON.stringify(action)).not.toContain("the-token");
    expect(t.accepts).toEqual([]);
    expect(sessionStorage.getItem(inviteTokenKey)).toBe("the-token");
  });

  it("with a session, sends the token once (StrictMode too), forgets it and goes to the tenant", async () => {
    const t = setup({ session: true });
    await vi.waitFor(() => expect(t.onAccepted).toHaveBeenCalledWith(tenantId));
    expect(t.accepts).toEqual([{ token: "the-token" }]);
    expect(sessionStorage.getItem(inviteTokenKey)).toBeNull();
  });

  it.each([
    [403, "invitation.email_mismatch"],
    [409, "invitation.already_accepted"],
    [409, "invitation.revoked"],
    [409, "invitation.expired"],
    [404, "invitation.not_found"],
    [409, "invitation.already_member"],
  ])("a %i %s forgets the token too and shows its text", async (status, code) => {
    const t = setup({ session: true, accept: () => json(status, { status, code }) });
    expect(await screen.findByRole("alert")).toHaveTextContent(texts.errors[code] ?? "?");
    expect(t.onOutcome).toHaveBeenCalledWith("failed", code);
    expect(t.onAccepted).not.toHaveBeenCalled();
    expect(t.accepts).toHaveLength(1);
    expect(sessionStorage.getItem(inviteTokenKey)).toBeNull();
  });

  it("the email mismatch explains which account to use", () => {
    expect(texts.errors["invitation.email_mismatch"]).toMatch(
      /entra con la cuenta del email invitado/,
    );
  });

  it("without a token, asks nothing and says so", async () => {
    sessionStorage.clear();
    const t = setup({ session: true });
    expect(await screen.findByText(texts.invite.missing)).toBeVisible();
    expect(vi.mocked(fetch)).not.toHaveBeenCalled();
    expect(t.accepts).toEqual([]);
  });
});
