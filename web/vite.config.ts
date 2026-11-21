import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The API in development. Proxying through Vite keeps the browser on one origin
// (http://localhost:5173), so the session cookie and the Origin check work as in
// production, where front end and API share a site.
const api = "http://localhost:8080";

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    // Platform routes are listed in src/platform/routes.ts; the app's are file-based
    // under src/app/routes, mounted inside /t/$tenantId (web/README.md).
    tanstackRouter({
      target: "react",
      routesDirectory: "./src",
      virtualRouteConfig: "./src/platform/routes.ts",
      generatedRouteTree: "./src/routeTree.gen.ts",
      autoCodeSplitting: true,
    }),
    react(),
    tailwindcss(),
  ],
  resolve: { tsconfigPaths: true },
  server: {
    proxy: {
      // The generated client calls /api/* (VITE_API_BASE_URL); the backend has no prefix.
      "/api": { target: api, rewrite: (path) => path.replace(/^\/api/, "") },
      // AuthKit returns to /auth/callback on this origin, so /auth is proxied as is.
      "/auth": { target: api },
    },
  },
});
