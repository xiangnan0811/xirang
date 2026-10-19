import type { MouseEvent, ReactElement, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { useNotificationContextOwner } from "@/hooks/use-notification-context-owner";
import {
  createNotificationConfigReturn,
  parseNotificationAlertId,
  readNotificationConfigReturn,
  type NotificationConfigTab,
} from "@/lib/notification-config-context";

export function NotificationConfigLink({
  tab,
  alertId,
  children,
}: {
  tab: NotificationConfigTab;
  alertId: string | null;
  children: ReactNode;
}): ReactElement | null {
  const owner = useNotificationContextOwner();
  if (!owner.active || owner.role !== "admin") {
    return null;
  }

  let canonical: string | null = null;
  if (alertId !== null) {
    canonical = parseNotificationAlertId(alertId);
    if (canonical === null) {
      return null;
    }
  }

  const payload = createNotificationConfigReturn(canonical);

  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (!owner.capture()() || (payload !== null && !readNotificationConfigReturn(payload))) {
      event.preventDefault();
    }
  };

  const href = `/app/settings?tab=${tab}`;

  return (
    <Button asChild variant="outline" size="sm">
      <Link
        to={href}
        state={payload !== null ? { notificationConfigReturn: payload } : undefined}
        onClick={handleClick}
      >
        {children}
      </Link>
    </Button>
  );
}

export function NotificationConfigLinks({
  alertId,
}: {
  alertId: string | null;
}): ReactElement | null {
  const { t } = useTranslation();
  const owner = useNotificationContextOwner();
  if (!owner.active || owner.role !== "admin") {
    return null;
  }

  let canonical: string | null = null;
  if (alertId !== null) {
    canonical = parseNotificationAlertId(alertId);
    if (canonical === null) {
      return null;
    }
  }

  return (
    <nav aria-label={t("notifications.configNavLabel")} className="flex flex-wrap items-center gap-2">
      <NotificationConfigLink tab="channels" alertId={canonical}>
        {t("notifications.configChannels")}
      </NotificationConfigLink>
      <NotificationConfigLink tab="silences" alertId={canonical}>
        {t("notifications.configSilences")}
      </NotificationConfigLink>
      <NotificationConfigLink tab="escalation" alertId={canonical}>
        {t("notifications.configEscalation")}
      </NotificationConfigLink>
      {canonical !== null ? (
        <span className="text-xs text-muted-foreground">
          {t("notifications.configContext.forAlert")}
        </span>
      ) : null}
    </nav>
  );
}
