import { useTranslation } from "react-i18next";
import { MoreHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import type { AlertRecord } from "@/types/domain";

export type AlertBulkActionsProps = {
  alert: AlertRecord;
  deliveryOpen: boolean;
  onRetry: (alert: AlertRecord) => void;
  onAck: (alert: AlertRecord) => void;
  onResolve: (alert: AlertRecord) => void;
  onToggleDeliveries: (alertId: string) => void;
};

export function AlertBulkActions({
  alert,
  deliveryOpen,
  onRetry,
  onAck,
  onResolve,
  onToggleDeliveries,
}: AlertBulkActionsProps) {
  const { t } = useTranslation();

  return (
    <div className="flex items-center gap-1">
      <Button
        size="sm"
        onClick={() => onRetry(alert)}
        disabled={!alert.retryable || !alert.taskId || alert.status === "resolved"}
      >
        {t("notifications.oneClickRetry")}
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" variant="outline" aria-label={t("common.more")}>
            <MoreHorizontal className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          <DropdownMenuItem
            disabled={alert.status !== "open"}
            onClick={() => onAck(alert)}
          >
            {t("notifications.markRead")}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={alert.status === "resolved"}
            onClick={() => onResolve(alert)}
          >
            {t("notifications.markResolved")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => onToggleDeliveries(alert.id)}>
            {deliveryOpen ? t("notifications.collapseDelivery") : t("notifications.deliveryRecords")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
