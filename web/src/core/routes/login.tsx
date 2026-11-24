import { createFileRoute } from "@tanstack/react-router";
import { getAuthLoginUrl } from "@/gen/api";
import { apiUrl } from "@/core/api/fetcher";
import { registeredApp } from "@/core/app-registry";
import { safeReturnTo } from "@/core/paths";
import { texts } from "@/core/texts";
import { Button } from "@/core/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/core/ui/card";

export const Route = createFileRoute("/login")({
  validateSearch: (search: Record<string, unknown>) => ({
    return_to: safeReturnTo(search["return_to"]),
  }),
  component: Login,
});

function Login() {
  const { return_to } = Route.useSearch();
  const { Logo, name } = registeredApp;

  // A top-level navigation: the API stores the intent and redirects to AuthKit, which
  // comes back to /auth/callback; the server validates return_to (auth-y-tenancy §5.1).
  function login() {
    window.location.assign(apiUrl(getAuthLoginUrl({ return_to })));
  }

  return (
    <main className="flex min-h-svh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <div className="mb-2 flex items-center gap-2">
            <Logo className="size-6" />
            <span className="font-heading font-semibold">{name}</span>
          </div>
          <CardTitle>{texts.login.title}</CardTitle>
          <CardDescription>{texts.login.description}</CardDescription>
        </CardHeader>
        <CardContent>
          <Button className="w-full" onClick={login}>
            {texts.login.button}
          </Button>
        </CardContent>
      </Card>
    </main>
  );
}
