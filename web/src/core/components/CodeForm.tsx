import { useId, useState } from "react";
import { useVerifyTOTP } from "@/gen/api";
import { texts } from "@/core/texts";
import { Button } from "@/core/ui/button";
import { Input } from "@/core/ui/input";
import { Label } from "@/core/ui/label";
import { ErrorText } from "./ErrorText";

/**
 * CodeForm asks for a TOTP code and verifies it. Each attempt uses up its challenge, right
 * or wrong (auth-y-tenancy §5.2.4), so `challenge` gives a fresh one for every attempt. On success
 * the session is replaced and the query cache invalidated (core/api/query-client.ts).
 */
export function CodeForm(props: { challenge: () => Promise<string>; onVerified: () => void }) {
  const id = useId();
  const [code, setCode] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const verify = useVerifyTOTP();

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const challengeId = await props.challenge();
      await verify.mutateAsync({ data: { challenge_id: challengeId, code } });
      props.onVerified();
    } catch (err) {
      setError(err);
      setCode("");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-3">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={id}>{texts.mfa.code}</Label>
        <Input
          id={id}
          name="code"
          value={code}
          onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))}
          inputMode="numeric"
          autoComplete="one-time-code"
          pattern="[0-9]{6}"
          required
          className="max-w-40 font-mono tracking-widest"
        />
      </div>
      <ErrorText error={error} />
      <div>
        <Button type="submit" disabled={busy || code.length !== 6}>
          {busy ? texts.mfa.verifying : texts.mfa.verify}
        </Button>
      </div>
    </form>
  );
}
