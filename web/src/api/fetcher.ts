// Base URL of the API (design, section 10): "/api" in development, where the Vite proxy
// strips it, and the API's own origin in production. The backend has no /api prefix.
const baseUrl = import.meta.env.VITE_API_BASE_URL.replace(/\/$/, "");

/** apiUrl turns a path of the generated client into a URL of the API. */
export function apiUrl(path: string): string {
  return `${baseUrl}${path}`;
}

/**
 * apiFetch is the fetch used by the generated client (orval mutator). It sends the
 * session cookie also when the API is on another origin of the same site.
 */
export async function apiFetch<T>(path: string, init: RequestInit): Promise<T> {
  const res = await fetch(apiUrl(path), { ...init, credentials: "include" });
  const text = [204, 205, 304].includes(res.status) ? "" : await res.text();
  const data: unknown = text ? JSON.parse(text) : {};
  return { data, status: res.status, headers: res.headers } as T;
}
