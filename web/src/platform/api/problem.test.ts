import { describe, expect, it } from "vitest";
import { parseProblem, parseRetryAfter } from "./problem";

describe("parseProblem", () => {
  it("reads the members the UI uses", () => {
    const p = parseProblem(
      JSON.stringify({
        type: "about:blank",
        title: "Unprocessable",
        status: 422,
        code: "tenant.slug_invalid",
        errors: [{ field: "slug", code: "tenant.slug_invalid", message: "x" }, { bad: 1 }],
        request_id: "r",
      }),
    );
    expect(p).toEqual({
      title: "Unprocessable",
      code: "tenant.slug_invalid",
      errors: [{ field: "slug", code: "tenant.slug_invalid", message: "x" }],
      request_id: "r",
    });
  });

  it("yields an empty problem for anything that is not one", () => {
    expect(parseProblem("")).toEqual({});
    expect(parseProblem("<html>")).toEqual({});
    expect(parseProblem("[1]")).toEqual({});
    expect(parseProblem('{"code":7}')).toEqual({});
  });
});

describe("parseRetryAfter", () => {
  it("reads seconds and HTTP dates", () => {
    const now = Date.parse("2026-11-20T10:00:00Z");
    expect(parseRetryAfter("120", now)).toBe(120);
    expect(parseRetryAfter("Fri, 20 Nov 2026 10:01:00 GMT", now)).toBe(60);
    expect(parseRetryAfter("Fri, 20 Nov 2026 09:00:00 GMT", now)).toBe(0);
    expect(parseRetryAfter(null, now)).toBeUndefined();
    expect(parseRetryAfter("soon", now)).toBeUndefined();
  });
});
