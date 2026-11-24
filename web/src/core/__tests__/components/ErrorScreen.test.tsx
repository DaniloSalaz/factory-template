import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { CoreErrorBoundary } from "@/core/components/ErrorScreen";
import { texts } from "@/core/texts";

function Broken(): never {
  throw new Error("secret detail for ana@example.com");
}

describe("CoreErrorBoundary", () => {
  it("shows the generic error screen, never the error", () => {
    // React logs the caught error to the console; it is expected here.
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <CoreErrorBoundary>
        <Broken />
      </CoreErrorBoundary>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(texts.errorScreen.title);
    expect(document.body.textContent).not.toContain("ana@example.com");
  });
});
