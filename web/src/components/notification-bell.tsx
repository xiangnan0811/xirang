import { useCallback } from "react";
import { Bell } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useAlertBell } from "@/hooks/use-alert-bell";
import { getSeverityMeta } from "@/lib/status";
import { formatRelativeTime } from "@/lib/date-utils";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

type NotificationBellProps = {
  token: string | null;
};

export function NotificationBell({ token }: NotificationBellProps) {
  const { t, i18n } = useTranslation();
  const locale = (i18n.language.startsWith("zh") ? "zh" : "en") as "zh" | "en";
  const navigate = useNavigate();
  const { unreadCount, recentAlerts, loading, fetchRecent } = useAlertBell(token);

  const handleOpenChange = useCallback(
    (open: boolean) => {
      if (open) {
        void fetchRecent();
      }
    },
    [fetchRecent]
  );

  const handleViewAll = useCallback(() => {
    navigate("/app/notifications");
  }, [navigate]);

  const countLabel = unreadCount.total > 99 ? "99+" : String(unreadCount.total);

  return (
    <DropdownMenu onOpenChange={handleOpenChange}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="relative size-8 text-muted-foreground hover:text-foreground !transition-none"
          aria-label={unreadCount.total > 0 ? t('notificationBell.labelWithCount', { count: unreadCount.total }) : t('notificationBell.label')}
        >
          <Bell className="size-4" />
          {unreadCount.total > 0 ? (
            <span className="absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-semibold leading-none text-destructive-foreground">
              {countLabel}
            </span>
          ) : null}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <div className="flex items-center justify-between px-2 py-1.5">
          <DropdownMenuLabel className="p-0 text-sm font-semibold">{t('notificationBell.label')}</DropdownMenuLabel>
          <div className="flex items-center gap-1.5">
            {unreadCount.critical > 0 ? (
              <Badge tone="destructive" className="h-5 px-1.5 text-[10px]">
                {t('notificationBell.critical')} {unreadCount.critical}
              </Badge>
            ) : null}
            {unreadCount.warning > 0 ? (
              <Badge tone="warning" className="h-5 px-1.5 text-[10px]">
                {t('notificationBell.warning')} {unreadCount.warning}
              </Badge>
            ) : null}
          </div>
        </div>
        <DropdownMenuSeparator />

        {loading ? (
          <div className="px-3 py-6 text-center text-xs text-muted-foreground">
            {t('common.loading')}
          </div>
        ) : recentAlerts.length === 0 ? (
          <div className="px-3 py-6 text-center text-xs text-muted-foreground">
            {t('notificationBell.noUnread')}
          </div>
        ) : (
          <div className="max-h-72 overflow-y-auto">
            {recentAlerts.map((alert) => {
              const severityMeta = getSeverityMeta(alert.severity);
              return (
                <DropdownMenuItem
                  key={alert.id}
                  className="flex cursor-pointer flex-col items-start gap-1 px-3 py-2"
                  onSelect={() => navigate(`/app/notifications?alert=${alert.id}`)}
                >
                  <div className="flex w-full items-center gap-2">
                    <Badge
                      tone={severityMeta.variant}
                      className="h-5 shrink-0 px-1.5 text-[10px]"
                    >
                      {severityMeta.label}
                    </Badge>
                    <span className="truncate text-xs font-medium">
                      {alert.nodeName}
                    </span>
                    <span className="ml-auto shrink-0 text-[10px] text-muted-foreground">
                      {alert.triggeredAt && alert.triggeredAt !== "-"
                        ? formatRelativeTime(alert.triggeredAt, locale)
                        : ""}
                    </span>
                  </div>
                  <p className="line-clamp-2 w-full text-xs text-muted-foreground">
                    {alert.message}
                  </p>
                </DropdownMenuItem>
              );
            })}
          </div>
        )}

        <DropdownMenuSeparator />
        <DropdownMenuItem
          className="justify-center text-xs text-primary"
          onSelect={handleViewAll}
        >
          {t('notificationBell.viewAll')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
