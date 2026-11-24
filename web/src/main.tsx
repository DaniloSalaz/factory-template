import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./core/styles.css";
import "./app/theme.css";
import { CoreProvider } from "./core/bootstrap";
import { CoreErrorBoundary } from "./core/components/ErrorScreen";
import { core, router } from "./router";

const root = document.getElementById("root");
if (!root) {
  throw new Error("missing #root element");
}

createRoot(root).render(
  <StrictMode>
    <CoreErrorBoundary>
      <CoreProvider value={core.context}>
        <RouterProvider router={router} />
      </CoreProvider>
    </CoreErrorBoundary>
  </StrictMode>,
);
