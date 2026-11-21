import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Me } from "@/gen/api";
import { createQueryClient } from "@/platform/api/query-client";
import { MfaVerify } from "@/platform/components/MfaVerify";
import { texts } from "@/platform/texts";

const factor = "0190a1b2-0000-7000-8000-0000000000f1";
const me: Me = {
  user: { id: "u", email: "ana@example.test", display_name: "Ana" },
  memberships: [],
  mfa_enrolled: true,
  mfa_factors: [{ id: factor, type: "totp", verified: true, created_at: "2026-11-01T10:00:00Z" }],
  session_mfa_at: null,
};

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status < 300 ? "application/json" : "application/problem+json" },
  });

afterEach(() => vi.unstubAllGlobals());

describe("MfaVerify", () => {
  it("uses a new challenge for every attempt and shows the error by its code", async () => {
    let challenges = 0;
    const verifies: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        const url = String(input);
        if (url === "/api/me") return json(200, me);
        if (url === "/api/me/mfa/challenges") {
          challenges++;
          return json(201, { challenge_id: `c${challenges}`, factor_id: factor, expires_at: "" });
        }
        if (url === "/api/me/mfa/totp/verify") {
          verifies.push(JSON.parse(String(init?.body)));
          return verifies.length === 1
            ? json(422, { title: "x", status: 422, code: "mfa.code_invalid" })
            : json(200, { mfa_verified_at: "2026-11-20T10:00:00Z" });
        }
        return json(404, {});
      }),
    );
    const onError = vi.fn();
    const client = createQueryClient({ onError, routeTenant: () => undefined });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const onVerified = vi.fn();
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={client}>
        <MfaVerify onVerified={onVerified} />
      </QueryClientProvider>,
    );

    const input = await screen.findByLabelText(texts.mfa.code);
    await user.type(input, "111111");
    await user.click(screen.getByRole("button", { name: texts.mfa.verify }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      texts.errors["mfa.code_invalid"] ?? "",
    );
    expect(onVerified).not.toHaveBeenCalled();

    await user.type(input, "222222");
    await user.click(screen.getByRole("button", { name: texts.mfa.verify }));
    await vi.waitFor(() => expect(onVerified).toHaveBeenCalledOnce());

    expect(verifies).toEqual([
      { challenge_id: "c1", code: "111111" },
      { challenge_id: "c2", code: "222222" },
    ]);
    expect(invalidate).toHaveBeenCalledWith();
  });
});
