/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Base URL of the API: "/api" in development, the API origin in production. */
  readonly VITE_API_BASE_URL: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
