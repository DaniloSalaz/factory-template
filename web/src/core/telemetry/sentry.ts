import * as Sentry from "@sentry/react";

// Error reports of the front end (ADR 0004). Only with VITE_SENTRY_DSN. No Session
// Replay (it records screens full of clients' data), no tracing and no traceparent to
// the API for now, and every event leaves without the user, headers, cookies, body,
// query string or fragment: the fragment of /invite holds an invitation's token.

type Event = Sentry.ErrorEvent;
type Breadcrumb = Sentry.Breadcrumb;

const allowedTags = ["tenant_id", "route"];
const allowedContexts = ["browser", "os", "device", "trace"];

/** stripUrl drops a URL's query string and fragment. */
export function stripUrl(url: string): string {
  const i = url.search(/[?#]/);
  return i === -1 ? url : url.slice(0, i);
}

/** redactText masks emails, key=value credentials and query strings in free text. */
export function redactText(text: string): string {
  return text
    .replace(/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g, "[email]")
    .replace(/\b(token|code|password|secret|api_?key|session)=[^\s&"'#]+/gi, "$1=[REDACTED]")
    .replace(/([?#])[A-Za-z0-9_.~%+-]+=[^\s"']*/g, "$1[REDACTED]");
}

/** scrubEvent is Sentry's beforeSend: ADR 0004 applied to an event. */
export function scrubEvent(event: Event): Event {
  delete event.user;
  if (event.request) {
    const url = event.request.url ? stripUrl(event.request.url) : undefined;
    event.request = url ? { url } : {};
  }
  if (event.transaction) {
    event.transaction = stripUrl(event.transaction);
  }
  delete event.breadcrumbs;
  delete event.extra;
  if (event.tags) {
    event.tags = Object.fromEntries(
      Object.entries(event.tags).filter(([k]) => allowedTags.includes(k)),
    );
  }
  if (event.contexts) {
    event.contexts = Object.fromEntries(
      Object.entries(event.contexts).filter(([k]) => allowedContexts.includes(k)),
    );
  }
  if (event.message) {
    event.message = redactText(event.message);
  }
  for (const ex of event.exception?.values ?? []) {
    if (ex.value) {
      ex.value = redactText(ex.value);
    }
  }
  return event;
}

/**
 * scrubBreadcrumb is Sentry's beforeBreadcrumb. Events drop their breadcrumbs anyway;
 * this keeps them clean while they wait in memory: URLs without query or fragment, no
 * data besides method and status, and messages redacted.
 */
export function scrubBreadcrumb(crumb: Breadcrumb): Breadcrumb | null {
  const data: Record<string, unknown> = {};
  for (const key of ["from", "to", "url"]) {
    const v = crumb.data?.[key];
    if (typeof v === "string") data[key] = stripUrl(v);
  }
  for (const key of ["method", "status_code"]) {
    if (crumb.data?.[key] !== undefined) data[key] = crumb.data[key];
  }
  const out: Breadcrumb = { ...crumb, data };
  if (crumb.message) out.message = redactText(crumb.message);
  return out;
}

export type SentrySettings = { dsn: string | undefined; release: string; environment: string };

/** initSentry starts error reporting if there is a DSN; otherwise it does nothing. */
export function initSentry(settings: SentrySettings): boolean {
  if (!settings.dsn) {
    return false;
  }
  Sentry.init({
    dsn: settings.dsn,
    release: settings.release,
    environment: settings.environment,
    // Collect nothing of the user or the requests; beforeSend removes it anyway.
    dataCollection: {
      userInfo: false,
      cookies: false,
      httpHeaders: false,
      httpBodies: [],
      urlQueryParams: false,
      stackFrameVariables: false,
    },
    // No tracing: no spans and no traceparent sent to the API (ADR 0004, for now).
    tracePropagationTargets: [],
    beforeSend: scrubEvent,
    beforeBreadcrumb: scrubBreadcrumb,
  });
  return true;
}

/** reportError sends an error the UI caught (router error component); no-op without Sentry. */
export function reportError(error: unknown): void {
  Sentry.captureException(error);
}
