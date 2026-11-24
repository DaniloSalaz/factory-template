import { errorMessage, requestIdOf } from "@/core/errors/message";
import { texts } from "@/core/texts";
import { Alert, AlertDescription } from "@/core/ui/alert";

/** ErrorText shows a failed request's message, unless the core already handles it. */
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
