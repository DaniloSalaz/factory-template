// The core's routes, for code that is not a <Link> (the error handler, redirects).
export const paths = {
  home: "/",
  login: "/login",
  tenants: "/tenants",
  mfa: "/account/mfa",
  mfaVerify: "/account/mfa/verify",
  mfaEnroll: "/account/mfa/enroll",
  invite: "/invite",
} as const;

/**
 * safeReturnTo keeps a return_to only if it is a path of this front end: the server
 * validates it again for /auth/login, but the router navigates to it directly.
 */
export function safeReturnTo(value: unknown): string {
  if (typeof value !== "string" || !value.startsWith("/") || value.startsWith("//")) {
    return paths.home;
  }
  if (value.includes("\\") || [...value].some((c) => c.charCodeAt(0) < 0x20)) {
    return paths.home;
  }
  return value;
}
