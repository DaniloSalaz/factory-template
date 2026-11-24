import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./core/styles.css";
import "./app/theme.css";
import { PlatformProvider } from "./core/bootstrap";
import { PlatformErrorBoundary } from "./core/components/ErrorScreen";
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
