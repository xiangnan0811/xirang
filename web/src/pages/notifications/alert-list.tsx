import { useTranslation } from "react-i18next";
import { ChevronDown, ChevronUp, Loader2 } from "lucide-react";
import { alertStatusMeta } from "@/pages/notifications-page.utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { getSeverityMeta } from "@/lib/status";
import type { AlertDeliveryRecord, AlertRecord } from "@/types/domain";
import { AlertBulkActions } from "./alert-bulk-actions";

type SortField = "triggered_at" | "severity" | "status" | "node_name";

export type AlertListProps = {
  alerts: AlertRecord[];
  highlightedAlertId: string | null;
  highlightRef: (alertId: string, el: HTMLElement | null) => void;
  viewMode: "list" | "cards";
  sortBy: SortField;
  sortOrder: "asc" | "desc";
  onToggleSort: (field: SortField) => void;
  deliveryOpenAlertId: string | null;
  deliveryLoadingAlertId: string | null;
  deliveryMap: Record<string, AlertDeliveryRecord[]>;
  retryingDeliveryKey: string | null;
  retryingAllAlertId: string | null;
  integrationNameMap: Map<string, string>;
  onRetry: (alert: AlertRecord) => void;
  onAck: (alert: AlertRecord) => void;
  onResolve: (alert: AlertRecord) => void;
  onToggleDeliveries: (alertId: string) => void;
  onRetryDelivery: (alertId: string, integrationId: string) => void;
  onRetryAllFailed: (alertId: string) => void;
};

export function AlertList({
  alerts,
  highlightedAlertId,
  highlightRef,
  viewMode,
  sortBy,
  sortOrder,
  onToggleSort,
  deliveryOpenAlertId,
  deliveryLoadingAlertId,
  deliveryMap,
  retryingDeliveryKey,
  retryingAllAlertId,
  integrationNameMap,
  onRetry,
  onAck,
  onResolve,
  onToggleDeliveries,
  onRetryDelivery,
  onRetryAllFailed,
}: AlertListProps) {
  const { t } = useTranslation();

  const sortIndicator = (field: SortField) => {
    if (sortBy !== field) return null;
    return sortOrder === "asc"
      ? <ChevronUp className="inline size-3.5" />
      : <ChevronDown className="inline size-3.5" />;
  };

  const sortableThProps = (field: SortField) => ({
    role: "button" as const,
    tabIndex: 0,
    className: "px-3 py-2.5 cursor-pointer select-none",
    onClick: () => onToggleSort(field),
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onToggleSort(field);
      }
    },
  });

  // --- 投递记录面板 ---
  const renderDeliveryPanel = (alert: AlertRecord) => {
    const isOpen = deliveryOpenAlertId === alert.id;
    if (!isOpen) return null;
    const deliveryPanelId = `alert-delivery-panel-${alert.id}`;
    return (
      <div
        id={deliveryPanelId}
        role="region"
        aria-label={t("notifications.deliveryPanelAriaLabel", { code: alert.errorCode })}
        aria-busy={deliveryLoadingAlertId === alert.id}
        className="mt-3 rounded-md border border-border bg-muted/25 p-2"
      >
        {deliveryLoadingAlertId === alert.id ? (
          <p className="text-xs text-muted-foreground">{t("notifications.deliveryLoading")}</p>
        ) : (deliveryMap[alert.id] ?? []).length ? (
          <div className="space-y-2">
            {(deliveryMap[alert.id] ?? []).some((d) => d.status === "failed") ? (
              <Button
                size="sm"
                variant="outline"
                disabled={retryingAllAlertId === alert.id}
                onClick={() => onRetryAllFailed(alert.id)}
              >
                {retryingAllAlertId === alert.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                {t("notifications.resendAllFailed")}
              </Button>
            ) : null}
            {(deliveryMap[alert.id] ?? []).map((delivery) => (
              <div key={delivery.id} className="rounded border border-border bg-card px-2 py-1.5 text-xs">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-medium">
                    {integrationNameMap.get(delivery.integrationId) ?? delivery.integrationId}
                  </span>
                  <Badge tone={delivery.status === "sent" ? "success" : "destructive"}>
                    {delivery.status === "sent" ? t("notifications.deliverySent") : t("notifications.deliveryFailed")}
                  </Badge>
                </div>
                <p className="mt-1 text-muted-foreground">{t("notifications.deliveryTime", { time: delivery.createdAt })}</p>
                {delivery.error ? <p className="mt-1 text-destructive">{t("notifications.deliveryError", { error: delivery.error })}</p> : null}
                {delivery.status === "failed" ? (
                  <Button
                    className="mt-2"
                    size="sm"
                    variant="outline"
                    disabled={retryingDeliveryKey === `${alert.id}:${delivery.integrationId}`}
                    onClick={() => onRetryDelivery(alert.id, delivery.integrationId)}
                  >
                    {retryingDeliveryKey === `${alert.id}:${delivery.integrationId}` && <Loader2 className="mr-1 size-4 animate-spin" />}
                    {t("notifications.resendNotification")}
                  </Button>
                ) : null}
              </div>
            ))}
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">{t("notifications.noDeliveryRecords")}</p>
        )}
      </div>
    );
  };

  // --- 卡片/时间线视图 ---
  const renderCardView = (items: AlertRecord[]) => (
    <div className="relative space-y-0 pl-6">
      {/* Left timeline rail */}
      <div className="absolute left-[7px] top-0 bottom-0 w-px bg-border" aria-hidden="true" />

      {items.map((alert) => {
        const severity = getSeverityMeta(alert.severity);
        const status = alertStatusMeta(alert.status);
        const dotClass =
          alert.severity === "critical"
            ? "bg-destructive"
            : alert.severity === "warning"
              ? "bg-warning"
              : "bg-info";

        return (
          <div key={alert.id} className="relative pb-3 last:pb-0">
            {/* Timeline dot */}
            <span
              className={`absolute -left-[19px] top-4 size-3 rounded-full border-2 border-background ${dotClass}`}
              aria-hidden="true"
            />

            <div
              ref={alert.id === highlightedAlertId ? (el) => highlightRef(alert.id, el) : undefined}
              className="rounded-lg border border-border bg-card shadow-sm overflow-hidden p-4 transition-colors hover:bg-muted/10"
            >
              {/* Timestamp → severity badge → status badge */}
              <div className="mb-2 flex flex-wrap items-center gap-2">
                <span className="text-xs text-muted-foreground">{alert.triggeredAt}</span>
                <Badge tone={severity.variant}>{severity.label}</Badge>
                <Badge tone={status.variant}>{status.label}</Badge>
              </div>

              <div className="flex flex-wrap items-start justify-between gap-2">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-foreground/90 truncate">{alert.nodeName}</span>
                  </div>
                  <p className="mt-1 text-sm">{alert.message}</p>
                  <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                    {alert.taskId ? (
                      <span>
                        {t("notifications.taskLabel", { id: alert.taskId })}
                        {alert.taskRunId ? ` ${t("notifications.taskRunLabel", { id: alert.taskRunId })}` : ""}
                      </span>
                    ) : (
                      <span>{t("notifications.nodeProbe")}</span>
                    )}
                    <span>{alert.policyName}</span>
                  </div>
                </div>
                <div className="shrink-0">
                  <AlertBulkActions
                    alert={alert}
                    deliveryOpen={deliveryOpenAlertId === alert.id}
                    onRetry={onRetry}
                    onAck={onAck}
                    onResolve={onResolve}
                    onToggleDeliveries={onToggleDeliveries}
                  />
                </div>
              </div>
              {renderDeliveryPanel(alert)}
            </div>
          </div>
        );
      })}
    </div>
  );

  // --- 表格视图 ---
  const renderTableView = (items: AlertRecord[]) => (
    <div className="rounded-lg border border-border bg-card overflow-x-auto">
      <table className="min-w-[960px] text-left text-sm w-full">
        <thead>
          <tr className="border-b border-border bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
            <th scope="col" {...sortableThProps("severity")} className="px-3 py-2.5 w-[80px] cursor-pointer select-none">
              {t("notifications.colSeverity")} {sortIndicator("severity")}
            </th>
            <th scope="col" {...sortableThProps("node_name")} className="px-3 py-2.5 w-[150px] cursor-pointer select-none">
              {t("notifications.colNode")} {sortIndicator("node_name")}
            </th>
            <th scope="col" className="px-3 py-2.5">
              {t("notifications.colMessage")}
            </th>
            <th scope="col" className="px-3 py-2.5 w-[120px]">{t("notifications.colPolicy")}</th>
            <th scope="col" {...sortableThProps("triggered_at")} className="px-3 py-2.5 w-[160px] cursor-pointer select-none">
              {t("notifications.colTime")} {sortIndicator("triggered_at")}
            </th>
            <th scope="col" {...sortableThProps("status")} className="px-3 py-2.5 w-[100px] cursor-pointer select-none">
              {t("notifications.colStatus")} {sortIndicator("status")}
            </th>
            <th scope="col" className="px-3 py-2.5 w-[120px]">{t("common.actions")}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((alert) => {
            const severity = getSeverityMeta(alert.severity);
            const status = alertStatusMeta(alert.status);
            return (
              <tr
                key={alert.id}
                ref={alert.id === highlightedAlertId ? (el) => highlightRef(alert.id, el) : undefined}
                className="border-b border-border/60 transition-colors duration-200 ease-out hover:bg-muted/40 group"
              >
                <td className="px-3 py-2.5">
                  <Badge tone={severity.variant}>{severity.label}</Badge>
                </td>
                <td className="px-3 py-2.5 font-medium">{alert.nodeName}</td>
                <td className="px-3 py-2.5 max-w-[300px] truncate" title={alert.message}>{alert.message}</td>
                <td className="px-3 py-2.5 text-muted-foreground text-xs">{alert.policyName}</td>
                <td className="px-3 py-2.5 text-muted-foreground text-xs">{alert.triggeredAt}</td>
                <td className="px-3 py-2.5">
                  <Badge tone={status.variant}>{status.label}</Badge>
                </td>
                <td className="px-3 py-2.5">
                  <AlertBulkActions
                    alert={alert}
                    deliveryOpen={deliveryOpenAlertId === alert.id}
                    onRetry={onRetry}
                    onAck={onAck}
                    onResolve={onResolve}
                    onToggleDeliveries={onToggleDeliveries}
                  />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {items.map((alert) => deliveryOpenAlertId === alert.id ? (
        <div key={`delivery-${alert.id}`} className="px-3 pb-3">
          {renderDeliveryPanel(alert)}
        </div>
      ) : null)}
    </div>
  );

  return (
    <>
      {/* 移动端始终卡片，桌面端按 viewMode 切换 */}
      <div className="md:hidden">
        {renderCardView(alerts)}
      </div>
      <div className="hidden md:block">
        {viewMode === "list" ? renderTableView(alerts) : renderCardView(alerts)}
      </div>
    </>
  );
}
