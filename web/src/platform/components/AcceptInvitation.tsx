import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { getGetMeQueryKey, useAcceptInvitation, useGetMe } from "@/gen/api";
import { ErrorText } from "@/platform/components/ErrorText";
import { errorCode } from "@/platform/errors/message";
import { hasInviteToken, takeInviteToken } from "@/platform/invite/invite-token";
import { texts } from "@/platform/texts";

export type AcceptOutcome = "missing" | "failed";

/**
 * AcceptInvitation is the second half of /invite (auth-y-tenancy §5.6.3): the token is already in
 * storage (invite-token.ts). GET /me checks the session first, so without one the error
 * handler sends to /login with return_to=/invite (the URL has no fragment by then) and
 * the token waits. With a session, the token leaves storage and goes to POST
 * /invitations/accept, once, whatever the answer.
 */
export function AcceptInvitation(props: {
  storage: Storage;
  onAccepted: (tenantId: string) => void;
  /** What to offer after a failure, by the error's code (logout for email_mismatch…). */
  onOutcome?: (outcome: AcceptOutcome, code: string | undefined) => void;
}) {
  const { storage, onAccepted, onOutcome } = props;
  const [waiting] = useState(() => hasInviteToken(storage));
  const [missing, setMissing] = useState(!waiting);
  const me = useGetMe({ query: { enabled: waiting } });
  const queryClient = useQueryClient();
  const accept = useAcceptInvitation();
  const { mutate } = accept;
  // Effects run twice under StrictMode; the token must be sent once.
  const started = useRef(false);

  useEffect(() => {
    if (!me.isSuccess || started.current) {
      return;
    }
    started.current = true;
    const token = takeInviteToken(storage);
    if (!token) {
      setMissing(true);
      onOutcome?.("missing", undefined);
      return;
    }
    mutate(
      { data: { token } },
      {
        onSuccess: async (res) => {
          // The new membership is in GET /me (the selector, the last tenant).
          await queryClient.invalidateQueries({ queryKey: getGetMeQueryKey() });
          onAccepted(res.tenant_id);
        },
        onError: (err) => onOutcome?.("failed", errorCode(err)),
      },
    );
  }, [me.isSuccess, storage, mutate, queryClient, onAccepted, onOutcome]);

  if (missing) {
    return <p>{texts.invite.missing}</p>;
  }
  if (me.isError) {
    return <ErrorText error={me.error} />;
  }
  if (accept.isError) {
    return <ErrorText error={accept.error} />;
  }
  return <p role="status">{texts.invite.accepting}</p>;
}
