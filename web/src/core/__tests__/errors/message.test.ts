import { describe, expect, it } from "vitest";
import { ApiError } from "@/platform/api/problem";
import { errorMessage } from "@/platform/errors/message";
import { texts } from "@/platform/texts";

describe("errorMessage", () => {
  it("shows nothing for errors the platform handles", () => {
    for (const e of [
      new ApiError(401, {}),
      new ApiError(403, { code: "auth.mfa_required" }),
      new ApiError(403, { code: "auth.mfa_enrollment_required" }),
      new ApiError(403, { code: "auth.tenant_forbidden" }),
      new ApiError(429, { code: "rate_limit.exceeded" }),
      null,
    ]) {
      expect(errorMessage(e)).toBeNull();
    }
  });

  it("shows the text of the code", () => {
    expect(errorMessage(new ApiError(409, { code: "tenant.slug_taken" }))).toBe(
      texts.errors["tenant.slug_taken"],
    );
    expect(errorMessage(new ApiError(403, { code: "mfa.last_factor_required" }))).toBe(
      texts.errors["mfa.last_factor_required"],
    );
  });

  // Members (auth-y-tenancy §5.7) and invitations (auth-y-tenancy §5.6): every code a screen shows has its own text.
  it.each([
    [409, "membership.last_owner"],
    [409, "membership.changed"],
    [403, "membership.target_forbidden"],
    [403, "membership.role_not_assignable"],
    [403, "membership.self_change"],
    [404, "membership.not_found"],
    [422, "membership.unknown_role"],
    [409, "invitation.pending"],
    [409, "invitation.already_member"],
    [403, "invitation.role_not_offerable"],
    [409, "invitation.revoked"],
    [422, "invitation.email_invalid"],
    [422, "invitation.unknown_role"],
    [403, "invitation.email_mismatch"],
    [409, "invitation.already_accepted"],
    [409, "invitation.expired"],
    [404, "invitation.not_found"],
    [403, "tenant.not_active"],
  ])("translates %i %s", (status, code) => {
    const text = texts.errors[code];
    expect(text).toBeTruthy();
    expect(errorMessage(new ApiError(status, { code }))).toBe(text);
    expect(text).not.toBe(texts.errors["validation"]);
    expect(text).not.toBe(texts.common.unexpected);
  });

  it("falls back on a generic text", () => {
    expect(errorMessage(new ApiError(422, { code: "something.new" }))).toBe(
      texts.errors["validation"],
    );
    expect(errorMessage(new ApiError(500, {}))).toBe(texts.common.unexpected);
    expect(errorMessage(new TypeError("Failed to fetch"))).toBe(texts.common.unexpected);
  });
});
