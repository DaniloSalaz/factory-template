import * as Sentry from "@sentry/react";
import type { ErrorComponentProps } from "@tanstack/react-router";
import { type ReactNode, useEffect } from "react";
import { reportError } from "@/platform/telemetry/sentry";
import { texts } from "@/platform/texts";
import { Button } from "@/platform/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/platform/ui/card";

/** ErrorScreen is the platform's generic screen for an error the UI did not expect. */
export function ErrorScreen() {
  return (
    <main className="flex min-h-svh items-center justify-center p-4">
      <Card className="w-full max-w-md" role="alert">
        <CardHeader>
          <CardTitle>{texts.errorScreen.title}</CardTitle>
          <CardDescription>{texts.errorScreen.description}</CardDescription>
        </CardHeader>
        <CardContent className="flex gap-2">
          <Button onClick={() => window.location.reload()}>{texts.errorScreen.reload}</Button>
          <Button variant="outline" onClick={() => window.location.assign("/")}>
            {texts.errorScreen.home}
          </Button>
        </CardContent>
      </Card>
    </main>
  );
}

/**
 * PlatformErrorBoundary catches render errors anywhere below it, reports them (if
 * Sentry is on) and shows ErrorScreen; it never shows the error itself.
 */
export function PlatformErrorBoundary(props: { children: ReactNode }) {
  return <Sentry.ErrorBoundary fallback={<ErrorScreen />}>{props.children}</Sentry.ErrorBoundary>;
}

/**
 * RouteErrorScreen is the router's error component: an error thrown while loading or
 * rendering a route is reported once and shows ErrorScreen.
 */
export function RouteErrorScreen(props: ErrorComponentProps) {
  useEffect(() => reportError(props.error), [props.error]);
  return <ErrorScreen />;
}
