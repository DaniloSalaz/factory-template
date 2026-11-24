import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { getGetMeQueryKey, type MFAFactor, useGetMe, useRemoveTOTP } from "@/gen/api";
import { usePlatform } from "@/platform/bootstrap";
import { ErrorText } from "@/platform/components/ErrorText";
import { Page } from "@/platform/components/Page";
import { paths } from "@/platform/paths";
import { formatDateTime, texts } from "@/platform/texts";
import { Badge } from "@/platform/ui/badge";
import { Button, buttonVariants } from "@/platform/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/platform/ui/card";

export const Route = createFileRoute("/_shell/account/mfa/")({
  component: Mfa,
});

function Mfa() {
  const me = useGetMe();
  return (
    <Page title={texts.mfa.title} description={texts.mfa.description}>
      {me.isPending && <p>{texts.common.loading}</p>}
      {me.isError && <ErrorText error={me.error} />}
      {me.isSuccess && (
        <>
          <p className="text-sm">
            {me.data.session_mfa_at
              ? texts.mfa.sessionVerifiedAt(formatDateTime(me.data.session_mfa_at))
              : texts.mfa.sessionNotVerified}
          </p>
          <Card>
            <CardHeader>
              <CardTitle>{texts.mfa.factors}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {me.data.mfa_factors.length === 0 && <p>{texts.mfa.noFactors}</p>}
              <ul className="flex flex-col gap-2">
                {me.data.mfa_factors.map((f) => (
                  <FactorRow key={f.id} factor={f} />
                ))}
              </ul>
              <div className="flex flex-wrap gap-2">
                <Link
                  to={paths.mfaEnroll}
                  search={{ return_to: paths.mfa }}
                  className={buttonVariants()}
                >
                  {texts.mfa.add}
                </Link>
                {me.data.mfa_enrolled && (
                  <Link
                    to={paths.mfaVerify}
                    search={{ return_to: paths.mfa }}
                    className={buttonVariants({ variant: "outline" })}
                  >
                    {texts.mfa.verifyNow}
                  </Link>
                )}
              </div>
            </CardContent>
          </Card>
        </>
      )}
    </Page>
  );
}

function FactorRow(props: { factor: MFAFactor }) {
  const { queryClient } = usePlatform();
  const [confirming, setConfirming] = useState(false);
  const remove = useRemoveTOTP({
    mutation: {
      onSuccess: () => queryClient.invalidateQueries({ queryKey: getGetMeQueryKey() }),
      onSettled: () => setConfirming(false),
    },
  });
  const f = props.factor;
  return (
    <li className="flex flex-col gap-2 rounded-lg border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex flex-col">
          <span className="font-medium">{texts.mfa.factorTotp}</span>
          <span className="text-sm text-muted-foreground">
            {texts.mfa.createdAt(formatDateTime(f.created_at))}
          </span>
        </span>
        <span className="flex items-center gap-2">
          <Badge variant={f.verified ? "default" : "secondary"}>
            {f.verified ? texts.mfa.verified : texts.mfa.pending}
          </Badge>
          {confirming ? (
            <>
              <span className="text-sm">{texts.mfa.confirmRemove}</span>
              <Button
                variant="destructive"
                size="sm"
                disabled={remove.isPending}
                onClick={() => remove.mutate({ factorId: f.id })}
              >
                {texts.mfa.confirmRemoveButton}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setConfirming(false)}>
                {texts.common.cancel}
              </Button>
            </>
          ) : (
            <Button variant="outline" size="sm" onClick={() => setConfirming(true)}>
              {texts.mfa.remove}
            </Button>
          )}
        </span>
      </div>
      <ErrorText error={remove.error} />
    </li>
  );
}
