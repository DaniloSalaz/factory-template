import { useSyncExternalStore } from "react";
import { usePlatform } from "@/core/bootstrap";
import { texts } from "@/core/texts";
import { Alert, AlertAction, AlertDescription } from "@/core/ui/alert";
import { Button } from "@/core/ui/button";

/** NoticeBar shows the platform's notice of the moment (today, a 429's wait). */
export function NoticeBar() {
  const { notices } = usePlatform();
  const notice = useSyncExternalStore(notices.subscribe, notices.current);
  if (!notice) {
    return null;
  }
  return (
    <Alert role="status">
      <AlertDescription>{texts.notices.rateLimited(notice.retryAfterSeconds)}</AlertDescription>
      <AlertAction>
        <Button variant="ghost" size="sm" onClick={() => notices.dismiss()}>
          {texts.common.dismiss}
        </Button>
      </AlertAction>
    </Alert>
  );
}
