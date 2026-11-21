import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./platform/styles.css";
import "./app/theme.css";
import { PlatformProvider } from "./platform/bootstrap";
import { PlatformErrorBoundary } from "./platform/components/ErrorScreen";
import { platform, router } from "./router";

const root = document.getElementById("root");
if (!root) {
  throw new Error("missing #root element");
}

createRoot(root).render(
  <StrictMode>
    <PlatformErrorBoundary>
      <PlatformProvider value={platform.context}>
        <RouterProvider router={router} />
      </PlatformProvider>
    </PlatformErrorBoundary>
  </StrictMode>,
);
