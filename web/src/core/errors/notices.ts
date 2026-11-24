// Notices are the core's messages that belong to no screen (today, only "wait before
// trying again"). The shell shows them; the error handler is the only one that adds them.

export type Notice = { kind: "rate_limited"; retryAfterSeconds: number | undefined };

export type Notices = {
  show(notice: Notice): void;
  dismiss(): void;
  current(): Notice | undefined;
  subscribe(listener: () => void): () => void;
};

export function createNotices(): Notices {
  let current: Notice | undefined;
  const listeners = new Set<() => void>();
  const emit = () => {
    for (const l of listeners) l();
  };
  return {
    show(notice) {
      current = notice;
      emit();
    },
    dismiss() {
      current = undefined;
      emit();
    },
    current: () => current,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}
