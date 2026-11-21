import { describe, expect, it } from "vitest";
import { safeReturnTo } from "./paths";

describe("safeReturnTo", () => {
  it("keeps paths of this front end", () => {
    expect(safeReturnTo("/t/abc?tab=1")).toBe("/t/abc?tab=1");
    expect(safeReturnTo("/")).toBe("/");
  });

  it("replaces anything else with /", () => {
    for (const v of [
      undefined,
      42,
      "",
      "https://evil.com",
      "//evil.com",
      "/\\evil.com",
      "/\t/evil.com",
      "evil.com",
    ]) {
      expect(safeReturnTo(v)).toBe("/");
    }
  });
});
