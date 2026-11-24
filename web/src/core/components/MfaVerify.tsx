import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useCreateMFAChallenge, useGetMe } from "@/gen/api";
import { paths } from "@/core/paths";
import { texts } from "@/core/texts";
import { buttonVariants } from "@/core/ui/button";
import { CodeForm } from "./CodeForm";
import { ErrorText } from "./ErrorText";

/**
 * MfaVerify verifies one of the user's verified factors: the answer to 403
 * auth.mfa_required, and "verify now" on the MFA screen.
 */
export function MfaVerify(props: { onVerified: () => void }) {
  const me = useGetMe();
  const create = useCreateMFAChallenge();
  const [chosen, setChosen] = useState<string>();

  if (me.isPending) {
    return <p>{texts.common.loading}</p>;
  }
  if (me.isError) {
    return <ErrorText error={me.error} />;
  }
  const factors = me.data.mfa_factors.filter((f) => f.verified);
  const factorId = chosen ?? factors[0]?.id;
  if (!factorId) {
    return (
      <div className="flex flex-col gap-3">
        <p>{texts.mfa.noVerifiedFactor}</p>
        <div>
          <Link to={paths.mfaEnroll} search={{ return_to: paths.mfa }} className={buttonVariants()}>
            {texts.mfa.add}
          </Link>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {factors.length > 1 && (
        <fieldset className="flex flex-col gap-1.5">
          <legend className="mb-1 text-sm font-medium">{texts.mfa.chooseFactor}</legend>
          {factors.map((f, i) => (
            <label key={f.id} className="flex items-center gap-2 text-sm">
              <input
                type="radio"
                name="factor"
                value={f.id}
                checked={f.id === factorId}
                onChange={() => setChosen(f.id)}
              />
              {texts.mfa.factorLabel(i + 1)}
            </label>
          ))}
        </fieldset>
      )}
      <CodeForm
        challenge={async () =>
          (await create.mutateAsync({ data: { factor_id: factorId } })).challenge_id
        }
        onVerified={props.onVerified}
      />
    </div>
  );
}
