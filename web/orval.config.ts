import { defineConfig } from "orval";

export default defineConfig({
  api: {
    input: { target: "../api-spec/openapi.yaml" },
    output: {
      target: "src/gen/api.ts",
      client: "fetch",
      mode: "single",
      clean: true,
      // Prepends VITE_API_BASE_URL to every path (design, section 10).
      override: { mutator: { path: "src/api/fetcher.ts", name: "apiFetch" } },
    },
  },
});
