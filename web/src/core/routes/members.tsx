import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import {
  getGetMeQueryKey,
  getListMembersQueryKey,
  listMembers,
  type Member,
  useChangeMemberRole,
  useGetMe,
  useLeaveTenant,
  useListTenantRoles,
  useRemoveMember,
} from "@/gen/api";
import { useCursorPages } from "@/platform/api/use-cursor-pages";
import { usePlatform } from "@/platform/bootstrap";
import { ErrorText } from "@/platform/components/ErrorText";
import { Page } from "@/platform/components/Page";
import { RoleSelect } from "@/platform/components/RoleSelect";
import { can, useCurrentTenant, useRouteTenantId } from "@/platform/tenant/use-tenant";
import { formatDateTime, texts } from "@/platform/texts";
import { Badge } from "@/platform/ui/badge";
import { Button } from "@/platform/ui/button";

// The members of the tenant (auth-y-tenancy §5.7): GET /tenant/members by cursor, the role change
// limited to the roles GET /tenant/roles says the caller may assign, removal and leaving.
// Hiding by permission is only UX: the API judges every change again.
export const Route = createFileRoute("/_shell/t/$tenantId/members")({
  component: Members,
});

function Members() {
  const tenant = useCurrentTenant();
  const me = useGetMe();
  const manage = can(tenant.data, "members:manage");
  const roles = useListTenantRoles();
  const list = useCursorPages(getListMembersQueryKey(), (cursor) =>
    listMembers(cursor ? { cursor } : undefined),
  );
  const assignable = roles.data?.items.filter((r) => r.can_assign).map((r) => r.name) ?? [];

  return (
    <Page title={texts.members.title} description={texts.members.description}>
      {list.isPending && <p>{texts.common.loading}</p>}
      <ErrorText error={list.error ?? roles.error} />
      {list.isSuccess && list.items.length === 0 && (
        <p className="text-muted-foreground">{texts.members.empty}</p>
      )}
      {list.items.length > 0 && (
        <ul className="flex flex-col gap-2" aria-label={texts.members.title}>
          {list.items.map((m) => (
            <MemberRow
              key={m.user_id}
              member={m}
              self={m.user_id === me.data?.user.id}
              manage={manage}
              assignable={assignable}
            />
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
            {texts.members.loadMore}
          </Button>
        </div>
      )}
    </Page>
  );
}

function MemberRow(props: {
  member: Member;
  self: boolean;
  manage: boolean;
  assignable: string[];
}) {
  const { member: m } = props;
  const who = m.display_name || m.email;
  return (
    <li className="flex flex-col gap-2 rounded-lg border p-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="flex flex-col">
          <span className="font-medium">
            {who}
            {props.self && (
              <Badge variant="secondary" className="ml-2">
                {texts.members.you}
              </Badge>
            )}
          </span>
          <span className="text-sm text-muted-foreground">
            {m.email} · {m.role} · {texts.members.joinedAt(formatDateTime(m.joined_at))}
          </span>
        </span>
        {props.self ? (
          <LeaveTenant />
        ) : (
          props.manage && <ManageMember member={m} who={who} assignable={props.assignable} />
        )}
      </div>
    </li>
  );
}

function useReloadMembers() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: getListMembersQueryKey() });
}

function ManageMember(props: { member: Member; who: string; assignable: string[] }) {
  const { member: m } = props;
  const reload = useReloadMembers();
  const [role, setRole] = useState(m.role);
  const [confirming, setConfirming] = useState(false);
  const change = useChangeMemberRole({ mutation: { onSuccess: reload } });
  const remove = useRemoveMember({ mutation: { onSuccess: reload } });
  // The current role stays selectable even when the caller may not assign it.
  const roles = props.assignable.includes(m.role)
    ? props.assignable
    : [m.role, ...props.assignable];

  return (
    <div className="flex flex-col items-end gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <RoleSelect
          label={texts.members.roleFor(props.who)}
          value={role}
          roles={roles}
          onChange={setRole}
          disabled={change.isPending}
        />
        <Button
          variant="outline"
          size="sm"
          disabled={change.isPending || role === m.role}
          onClick={() => {
            remove.reset();
            change.mutate({ userId: m.user_id, data: { role } });
          }}
        >
          {change.isPending ? texts.members.changing : texts.members.changeRole}
        </Button>
        {confirming ? (
          <>
            <span className="text-sm">{texts.members.confirmRemove(props.who)}</span>
            <Button
              variant="destructive"
              size="sm"
              disabled={remove.isPending}
              onClick={() => {
                change.reset();
                remove.mutate({ userId: m.user_id });
              }}
            >
              {texts.members.confirmRemoveButton}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)}>
              {texts.common.cancel}
            </Button>
          </>
        ) : (
          <Button variant="outline" size="sm" onClick={() => setConfirming(true)}>
            {texts.members.remove}
          </Button>
        )}
      </div>
      <ErrorText error={change.error ?? remove.error} />
    </div>
  );
}

function LeaveTenant() {
  const tenantId = useRouteTenantId();
  const { lastTenant } = usePlatform();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  const leave = useLeaveTenant({
    mutation: {
      onSuccess: async () => {
        if (lastTenant.get() === tenantId) {
          lastTenant.clear();
        }
        // Away from /t/$tenantId first: its GET /tenant would now answer 403.
        await navigate({ to: "/tenants" });
        await queryClient.invalidateQueries({ queryKey: getGetMeQueryKey() });
      },
    },
  });

  return (
    <div className="flex flex-col items-end gap-2">
      <div className="flex flex-wrap items-center gap-2">
        {confirming ? (
          <>
            <span className="text-sm">{texts.members.confirmLeave}</span>
            <Button
              variant="destructive"
              size="sm"
              disabled={leave.isPending}
              onClick={() => leave.mutate()}
            >
              {texts.members.confirmLeaveButton}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(false)}>
              {texts.common.cancel}
            </Button>
          </>
        ) : (
          <Button variant="outline" size="sm" onClick={() => setConfirming(true)}>
            {texts.members.leave}
          </Button>
        )}
      </div>
      <ErrorText error={leave.error} />
    </div>
  );
}
