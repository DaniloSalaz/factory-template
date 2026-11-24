import { useRef, useState } from "react";
import { useCreateMFAChallenge, useEnrollTOTP } from "@/gen/api";
import { texts } from "@/core/texts";
import { Button } from "@/core/ui/button";
import { CodeForm } from "./CodeForm";
import { ErrorText } from "./ErrorText";

// What the screen keeps of an enrollment: never the otpauth URI, and the QR code and the
// secret only in this component's state, gone when it unmounts. Nothing is stored or logged.
type Shown = { factorId: string; qrCode: string; secret: string };
type FirstChallenge = { id: string; expiresAt: number };

/**
 * MfaEnroll enrolls a TOTP factor: POST /me/mfa/totp only when the user asks (never on
 * mount), shows its QR code and secret once and verifies its first code.
 */
export function MfaEnroll(props: { onEnrolled: () => void }) {
  const enroll = useEnrollTOTP();
  const create = useCreateMFAChallenge();
  const [shown, setShown] = useState<Shown | null>(null);
  // The enrollment's own challenge serves the first attempt; later ones get a new one.
  const first = useRef<FirstChallenge | null>(null);

  async function start() {
    try {
      const e = await enroll.mutateAsync();
      first.current = { id: e.challenge_id, expiresAt: Date.parse(e.expires_at) };
      setShown({ factorId: e.factor_id, qrCode: e.qr_code, secret: e.secret });
    } catch {
      // enroll.error has it; ErrorText shows it unless the platform handles it.
    }
  }

  async function challenge(factorId: string): Promise<string> {
    const c = first.current;
    first.current = null;
    if (c && c.expiresAt > Date.now()) {
      return c.id;
    }
    return (await create.mutateAsync({ data: { factor_id: factorId } })).challenge_id;
  }

  if (!shown) {
    return (
      <div className="flex flex-col gap-3">
        <ErrorText error={enroll.error} />
        <div>
          <Button onClick={start} disabled={enroll.isPending}>
            {enroll.isPending ? texts.mfa.enrollStarting : texts.mfa.enrollStart}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <p>{texts.mfa.enrollScan}</p>
      <img
        src={shown.qrCode}
        alt={texts.mfa.enrollQrAlt}
        width={192}
        height={192}
        className="rounded-md border"
      />
      <div className="flex flex-col gap-1 text-sm">
        <p>{texts.mfa.enrollSecret}</p>
        <code className="w-fit rounded bg-muted px-2 py-1 font-mono break-all" data-testid="secret">
          {shown.secret}
        </code>
        <p className="text-muted-foreground">{texts.mfa.enrollOnce}</p>
      </div>
      <p>{texts.mfa.enrollCode}</p>
      <CodeForm
        challenge={() => challenge(shown.factorId)}
        onVerified={() => {
          setShown(null);
          props.onEnrolled();
        }}
      />
    </div>
  );
}
