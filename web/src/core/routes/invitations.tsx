import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useId, useState } from "react";
import {
  getListInvitationsQueryKey,
  type Invitation,
  listInvitations,
  useCreateInvitation,
  useListTenantRoles,
  useResendInvitation,
  useRevokeInvitation,
} from "@/gen/api";
import { useCursorPages } from "@/platform/api/use-cursor-pages";
import { ErrorText } from "@/platform/components/ErrorText";
import { Page } from "@/platform/components/Page";
import { RoleSelect } from "@/platform/components/RoleSelect";
import { errorCode, errorMessage } from "@/platform/errors/message";
import { formatDateTime, texts } from "@/platform/texts";
import { Alert, AlertDescription } from "@/platform/ui/alert";
import { Badge } from "@/platform/ui/badge";
import { Button } from "@/platform/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/platform/ui/card";
import { Input } from "@/platform/ui/input";
import { Label } from "@/platform/ui/label";

// The tenant's invitations (auth-y-tenancy §5.6): pending and expired ones by cursor, inviting
// with a role GET /tenant/roles says the caller may offer, resending (a new token and
// expiry) and revoking. The link only ever goes to the invitee's email.
export const Route = createFileRoute("/_shell/t/$tenantId/invitations")({
  component: Invitations,
});

function useReloadInvitations() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: getListInvitationsQueryKey() });
}

function Invitations() {
  const list = useCursorPages(getListInvitationsQueryKey(), (cursor) =>
    listInvitations(cursor ? { cursor } : undefined),
  );
  return (
    <Page title={texts.invitations.title} description={texts.invitations.description}>
      <InviteForm pending={list.items} />
      {list.isPending && <p>{texts.common.loading}</p>}
      <ErrorText error={list.error} />
      {list.isSuccess && list.items.length === 0 && (
        <p className="text-muted-foreground">{texts.invitations.empty}</p>
      )}
      {list.items.length > 0 && (
        <ul className="flex flex-col gap-2" aria-label={texts.invitations.title}>
          {list.items.map((inv) => (
            <InvitationRow key={inv.id} invitation={inv} />
          ))}
        </ul>
      )}
      {list.hasNextPage && (
        <div>
          <Button
            variant="outline"
            disabled={list.isFetchingNextPage}
            onClick={() => void list.fetchNextPage()}
          >
            {texts.invitations.loadMore}
          </Button>
        </div>
      )}
    </Page>
  );
}

function InviteForm(props: { pending: Invitation[] }) {
  const emailId = useId();
  const roleId = useId();
  const roles = useListTenantRoles();
  const offerable = roles.data?.items.filter((r) => r.can_offer).map((r) => r.name) ?? [];
  const [email, setEmail] = useState("");
  const [picked, setPicked] = useState<string | undefined>();
  // The least powerful offerable role by default: the list comes most permissions first.
  const role = picked ?? offerable.at(-1) ?? "";
  const [sent, setSent] = useState<string | null>(null);
  const reload = useReloadInvitations();
  const create = useCreateInvitation({
    mutation: {
      onSuccess: async (inv) => {
        setSent(texts.invitations.invited(inv.email));
        setEmail("");
        await reload();
      },
    },
  });
  const resend = useResendInvitation({
    mutation: {
      onSuccess: async () => {
        create.reset();
        setSent(texts.invitations.resent);
        await reload();
      },
    },
  });

  const code = errorCode(create.error);
  const emailError = code === "invitation.email_invalid" ? errorMessage(create.error) : null;
  // invitation.pending: the pending one is resent instead, if it is among those loaded.
  const pending =
    code === "invitation.pending"
      ? props.pending.find((i) => i.email.toLowerCase() === email.trim().toLowerCase())
      : undefined;

  if (roles.isSuccess && offerable.length === 0) {
    return <p className="text-muted-foreground">{texts.invitations.noOfferableRoles}</p>;
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>{texts.invitations.inviteTitle}</CardTitle>
        <CardDescription>{texts.invitations.inviteDescription}</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            setSent(null);
            resend.reset();
            create.mutate({ data: { email, role } });
          }}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={emailId}>{texts.invitations.email}</Label>
            <Input
              id={emailId}
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              aria-invalid={emailError ? true : undefined}
              aria-describedby={emailError ? `${emailId}-error` : undefined}
              autoCapitalize="none"
              spellCheck={false}
              required
            />
            {emailError && (
              <p id={`${emailId}-error`} className="text-sm text-destructive">
                {emailError}
              </p>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={roleId}>{texts.invitations.roleLabel}</Label>
            <div>
              <RoleSelect id={roleId} value={role} roles={offerable} onChange={setPicked} />
            </div>
          </div>
          {!emailError && <ErrorText error={create.error ?? resend.error ?? roles.error} />}
          {pending && (
            <div>
              <Button
                type="button"
                variant="outline"
                disabled={resend.isPending}
                onClick={() => resend.mutate({ id: pending.id })}
              >
                {texts.invitations.resendPending}
              </Button>
            </div>
          )}
          {sent && (
            <Alert role="status">
              <AlertDescription>{sent}</AlertDescription>
            </Alert>
          )}
          <div>
            <Button type="submit" disabled={create.isPending || !role}>
              {create.isPending ? texts.invitations.inviting : texts.invitations.invite}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function InvitationRow(props: { invitation: Invitation }) {
  const inv = props.invitation;
  const reload = useReloadInvitations();
  const [confirming, setConfirming] = useState(false);
  const [resent, setResent] = useState(false);
  const resend = useResendInvitation({
    mutation: {
      onSuccess: async () => {
        setResent(true);
        await reload();
      },
    },
  });
  const revoke = useRevokeInvitation({ mutation: { onSuccess: reload } });

  return (
    <li className="flex flex-col gap-2 rounded-lg border p-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="flex flex-col">
          <span className="font-medium">
            {inv.email}
            {inv.expired && (
              <Badge variant="secondary" className="ml-2">
                {texts.invitations.expired}
              </Badge>
            )}
          </span>
          <span className="text-sm text-muted-foreground">
            {texts.invitations.role(inv.role)}
            {!inv.expired && ` · ${texts.invitations.expiresAt(formatDateTime(inv.expires_at))}`}
          </span>
        </span>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={resend.isPending}
            onClick={() => {
              revoke.reset();
              resend.mutate({ id: inv.id });
            }}
          >
            {texts.invitations.resend}
          </Button>
          {confirming ? (
            <>
              <span className="text-sm">{texts.invitations.confirmRevoke(inv.email)}</span>
              <Button
                variant="destructive"
                size="sm"
                disabled={revoke.isPending}
                onClick={() => {
                  resend.reset();
                  revoke.mutate({ id: inv.id });
                }}
              >
                {texts.invitations.confirmRevokeButton}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setConfirming(false)}>
                {texts.common.cancel}
              </Button>
            </>
          ) : (
            <Button variant="outline" size="sm" onClick={() => setConfirming(true)}>
              {texts.invitations.revoke}
            </Button>
          )}
        </div>
      </div>
      {resent && !resend.isError && (
        <p role="status" className="text-sm text-muted-foreground">
          {texts.invitations.resent}
        </p>
      )}
      <ErrorText error={resend.error ?? revoke.error} />
    </li>
  );
}
