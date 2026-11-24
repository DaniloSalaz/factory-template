import { createFileRoute, Link, Outlet, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { registeredApp } from "@/core/app-registry";
import { useCore } from "@/core/bootstrap";
import { ErrorText } from "@/core/components/ErrorText";
import { NoticeBar } from "@/core/components/NoticeBar";
import { errorMessage } from "@/core/errors/message";
import { texts } from "@/core/texts";
import { Button, buttonVariants } from "@/core/ui/button";
import { type Logout, useAuthLogout, useAuthLogoutAll, useGetMe } from "@/gen/api";

export const Route = createFileRoute("/_shell")({
  component: Shell,
});

// The layout of every signed-in screen. GET /me is the session check: a 401 sends to
// /login (core/errors/handle.ts), so nothing below renders without a session.
function Shell() {
  const me = useGetMe();
  const { Logo, name } = registeredApp;
  return (
    <div className="flex min-h-svh flex-col">
      <header className="border-b">
        <div className="mx-auto flex w-full max-w-5xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
          <Link to="/" className="flex items-center gap-2 font-heading font-semibold">
            <Logo className="size-6" />
            {name}
          </Link>
          <nav aria-label={texts.shell.mainNav} className="flex items-center gap-1">
            <Link to="/tenants" className={buttonVariants({ variant: "ghost", size: "sm" })}>
              {texts.shell.tenants}
            </Link>
            <Link to="/account/mfa" className={buttonVariants({ variant: "ghost", size: "sm" })}>
              {texts.shell.security}
            </Link>
          </nav>
          <div className="ml-auto flex items-center gap-2 text-sm">
            {me.data && <span className="text-muted-foreground">{me.data.user.email}</span>}
            <LogoutButtons />
          </div>
        </div>
      </header>
      <div className="mx-auto w-full max-w-5xl px-4 pt-4">
        <NoticeBar />
      </div>
      <main className="mx-auto w-full max-w-5xl flex-1 px-4 py-6">
        {me.isPending && <p>{texts.common.loading}</p>}
        {me.isError && <ErrorText error={me.error} />}
        {me.isSuccess && <Outlet />}
      </main>
    </div>
  );
}

function LogoutButtons() {
  const { queryClient } = useCore();
  const navigate = useNavigate();
  const logout = useAuthLogout();
  const logoutAll = useAuthLogoutAll();
  const [error, setError] = useState<unknown>(null);

  // After the API revokes the session, logout_url also ends the AuthKit session and comes
  // back to the front end (its Sign-out URI); without one, straight to /login.
  function leave(res: Logout) {
    queryClient.clear();
    if (res.logout_url) {
      window.location.assign(res.logout_url);
      return;
    }
    void navigate({ to: "/login", search: { return_to: "/" } });
  }

  const onError = (err: unknown) => setError(err);
  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => logout.mutate(undefined, { onSuccess: leave, onError })}
      >
        {texts.shell.logout}
      </Button>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => logoutAll.mutate(undefined, { onSuccess: leave, onError })}
      >
        {texts.shell.logoutAll}
      </Button>
      {errorMessage(error) && (
        <span role="alert" className="text-destructive">
          {texts.shell.logoutFailed}
        </span>
      )}
    </>
  );
}
