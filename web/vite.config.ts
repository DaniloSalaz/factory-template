import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The API in development. Proxying through Vite keeps the browser on one origin
// (http://localhost:5173), so the session cookie and the Origin check work as in
// production, where front end and API share a site.
const api = "http://localhost:8080";

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/auth": { target: api },
      "/api": { target: api, rewrite: (path) => path.replace(/^\/api/, "") },
    },
  },
});
