/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Base URL of the API: "/api" in development, the API origin in production. */
  readonly VITE_API_BASE_URL: string;
  /** Sentry DSN of the front end; empty or absent, no reports are sent (ADR 0004). */
  readonly VITE_SENTRY_DSN?: string;
  /** The build's version (the commit), for Sentry's release; "dev" when absent. */
  readonly VITE_RELEASE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
