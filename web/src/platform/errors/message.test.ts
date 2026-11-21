import { describe, expect, it } from "vitest";
import { ApiError } from "@/platform/api/problem";
import { texts } from "@/platform/texts";
import { errorMessage } from "./message";

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

  it("falls back on a generic text", () => {
    expect(errorMessage(new ApiError(422, { code: "something.new" }))).toBe(
      texts.errors["validation"],
    );
    expect(errorMessage(new ApiError(500, {}))).toBe(texts.common.unexpected);
    expect(errorMessage(new TypeError("Failed to fetch"))).toBe(texts.common.unexpected);
  });
});
