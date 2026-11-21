import { paths } from "@/platform/paths";

// The token of an invitation link (/invite#token=…, design 5.6.3). The fragment never
// reaches a server nor a Referer, and it must not stay in the URL either: the history,
// a shared screenshot or return_to would carry it. So it moves to sessionStorage (this
// tab only, gone when it closes) before the router or any request starts, and leaves it
// once, when it is sent to POST /invitations/accept.

export const inviteTokenKey = "platform.inviteToken";

type Win = Pick<Window, "location" | "history" | "sessionStorage">;

/**
 * captureInviteToken runs first thing at start-up (src/router.ts). On /invite it keeps
 * the fragment's token, if any, and removes the fragment from the URL whatever it holds,
 * without a navigation. Elsewhere it does nothing.
 */
export function captureInviteToken(win: Win): void {
  const { pathname, search, hash } = win.location;
  if (pathname !== paths.invite || hash === "") {
    return;
  }
  win.history.replaceState(win.history.state, "", pathname + search);
  const token = new URLSearchParams(hash.slice(1)).get("token");
  if (token) {
    try {
      win.sessionStorage.setItem(inviteTokenKey, token);
    } catch {
      // Storage disabled: the page says there is no invitation to accept.
    }
  }
}

/** hasInviteToken says whether a token is waiting to be accepted. */
export function hasInviteToken(storage: Storage): boolean {
  try {
    return Boolean(storage.getItem(inviteTokenKey));
  } catch {
    return false;
  }
}

/** takeInviteToken returns the waiting token and forgets it, whatever happens next. */
export function takeInviteToken(storage: Storage): string | undefined {
  try {
    const token = storage.getItem(inviteTokenKey) ?? undefined;
    storage.removeItem(inviteTokenKey);
    return token;
  } catch {
    return undefined;
  }
}
