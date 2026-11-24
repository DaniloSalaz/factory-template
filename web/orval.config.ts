import { defineConfig } from "orval";

export default defineConfig({
  api: {
    input: { target: "../api-spec/openapi.gen.yaml" },
    output: {
      target: "src/gen/api.ts",
      client: "react-query",
      httpClient: "fetch",
      mode: "single",
      clean: true,
      override: {
        // Prepends VITE_API_BASE_URL, adds X-Tenant-ID where the contract asks for it and
        // turns every non-2xx answer into an ApiError (web/README.md).
        mutator: { path: "src/core/api/fetcher.ts", name: "apiFetch" },
        // apiFetch throws on errors, so a call resolves only with a success.
        fetch: { includeHttpResponseReturnType: false },
      },
    },
  },
});
