import { errorMessage, requestIdOf } from "@/platform/errors/message";
import { texts } from "@/platform/texts";
import { Alert, AlertDescription } from "@/platform/ui/alert";

/** ErrorText shows a failed request's message, unless the platform already handles it. */
export function ErrorText(props: { error: unknown }) {
  const message = errorMessage(props.error);
  if (!message) {
    return null;
  }
  const requestId = requestIdOf(props.error);
  return (
    <Alert variant="destructive" role="alert">
      <AlertDescription>
        {message}
        {requestId && <span className="block text-xs">{texts.common.requestId(requestId)}</span>}
      </AlertDescription>
    </Alert>
  );
}
