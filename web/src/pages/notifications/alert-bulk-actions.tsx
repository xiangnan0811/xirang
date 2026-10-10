import { useRef, type MutableRefObject } from "react";
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

/** Marks the More button so silence focus can find the current row, not a detached node. */
export const SILENCE_ALERT_TRIGGER_ATTRIBUTE = "data-silence-alert";

export type AlertBulkActionsProps = {
  alert: AlertRecord;
  deliveryOpen: boolean;
  canWriteAlerts: boolean;
  canTriggerTasks: boolean;
  canManageSilences: boolean;
  onRetry: (alert: AlertRecord) => void;
  onAck: (alert: AlertRecord) => void;
  onResolve: (alert: AlertRecord) => void;
  onResolveNodeAlerts: (alert: AlertRecord) => void;
  onToggleDeliveries: (alertId: string) => void;
  onSilence: (alert: AlertRecord) => void;
  silenceReturnRef: MutableRefObject<HTMLButtonElement | null>;
};

export function AlertBulkActions({
  alert,
  deliveryOpen,
  canWriteAlerts,
  canTriggerTasks,
  canManageSilences,
  onRetry,
  onAck,
  onResolve,
  onResolveNodeAlerts,
  onToggleDeliveries,
  onSilence,
  silenceReturnRef,
}: AlertBulkActionsProps) {
  const { t } = useTranslation();
  const triggerRef = useRef<HTMLButtonElement>(null);

  const requestSilence = () => {
    silenceReturnRef.current = triggerRef.current;
    onSilence(alert);
  };

  return (
    <div className="flex items-center gap-1">
      {canTriggerTasks ? (
        <Button
          size="sm"
          onClick={() => onRetry(alert)}
          disabled={!alert.retryable || !alert.taskId || alert.status === "resolved"}
        >
          {t("notifications.oneClickRetry")}
        </Button>
      ) : null}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            ref={triggerRef}
            size="sm"
            variant="outline"
            aria-label={t("common.more")}
            {...{ [SILENCE_ALERT_TRIGGER_ATTRIBUTE]: alert.id }}
          >
            <MoreHorizontal className="size-4" aria-hidden="true" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="start"
          onCloseAutoFocus={(event) => {
            // The silence dialogs are conditional and have no DialogTrigger.
            // Let the open dialog keep focus; otherwise return to this More button.
            if (document.querySelector('[role="dialog"]')) event.preventDefault();
          }}
        >
          {canWriteAlerts ? (
            <>
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
              <DropdownMenuItem
                disabled={alert.status === "resolved" || alert.nodeId === 0}
                onClick={() => onResolveNodeAlerts(alert)}
              >
                {t("notifications.resolveNodeAlerts")}
              </DropdownMenuItem>
            </>
          ) : null}
          {canManageSilences ? (
            <DropdownMenuItem onSelect={requestSilence}>
              {t("notifications.silenceCategory")}
            </DropdownMenuItem>
          ) : null}
          <DropdownMenuItem onClick={() => onToggleDeliveries(alert.id)}>
            {deliveryOpen ? t("notifications.collapseDelivery") : t("notifications.deliveryRecords")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
