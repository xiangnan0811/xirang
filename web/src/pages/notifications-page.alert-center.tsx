import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  BellRing,
  Loader2,
  MoreHorizontal,
  RefreshCw,
} from "lucide-react";
import {
  alertStatusMeta,
  severityToTone,
  severityWeight,
  statusWeight,
} from "@/pages/notifications-page.utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { AppSelect } from "@/components/ui/app-select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { SearchInput } from "@/components/ui/search-input";
import { StatusPulse } from "@/components/status-pulse";
import { toast } from "@/components/ui/toast";
import { usePageFilters } from "@/hooks/use-page-filters";
import { getSeverityMeta } from "@/lib/status";
import { getErrorMessage } from "@/lib/utils";
import type { AlertDeliveryRecord, AlertRecord } from "@/types/domain";

type AlertCenterProps = {
  alerts: AlertRecord[];
  integrations: { id: string; name: string }[];
  loading: boolean;
  globalSearch: string;
  setGlobalSearch: (value: string) => void;
  retryAlert: (id: string) => Promise<void>;
  acknowledgeAlert: (id: string) => Promise<void>;
  resolveAlert: (id: string) => Promise<void>;
  fetchAlertDeliveries: (alertId: string) => Promise<AlertDeliveryRecord[]>;
  retryAlertDelivery: (alertId: string, integrationId: string) => Promise<{ message: string }>;
  retryFailedAlertDeliveries: (alertId: string) => Promise<{ message: string }>;
  initialAlertId?: string | null;
  onAlertHighlighted?: () => void;
};

export function AlertCenter({
  alerts,
  integrations,
  loading,
  globalSearch,
  setGlobalSearch,
  retryAlert,
  acknowledgeAlert,
  resolveAlert,
  fetchAlertDeliveries,
  retryAlertDelivery,
  retryFailedAlertDeliveries,
  initialAlertId,
  onAlertHighlighted,
}: AlertCenterProps) {
  const { t } = useTranslation();
  const {
    keyword, setKeyword,
    severity: severityFilter, setSeverity: setSeverityFilter,
    status: statusFilter, setStatus: setStatusFilter,
    deferredKeyword,
    reset: resetFilters,
  } = usePageFilters({
    keyword: { key: "xirang.notifications.keyword", default: "" },
    severity: { key: "xirang.notifications.severity", default: "all" },
    status: { key: "xirang.notifications.status", default: "all" },
  }, globalSearch, setGlobalSearch);
  const [deliveryOpenAlertId, setDeliveryOpenAlertId] = useState<string | null>(null);
  const [deliveryLoadingAlertId, setDeliveryLoadingAlertId] = useState<string | null>(null);
  const [deliveryMap, setDeliveryMap] = useState<Record<string, AlertDeliveryRecord[]>>({});
  const [retryingDeliveryKey, setRetryingDeliveryKey] = useState<string | null>(null);
  const [retryingAllAlertId, setRetryingAllAlertId] = useState<string | null>(null);
  const highlightRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (initialAlertId && alerts.length > 0) {
      // 清空筛选条件，确保目标告警不会被过滤掉
      resetFilters();
      setDeliveryOpenAlertId(initialAlertId);
      if (!deliveryMap[initialAlertId]) {
        refreshDeliveries(initialAlertId);
      }
      onAlertHighlighted?.();
      // Scroll into view after a short delay
      setTimeout(() => {
        highlightRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
      }, 100);
    }
  }, [initialAlertId, alerts.length]);

  const integrationNameMap = useMemo(
    () => new Map(integrations.map((i) => [i.id, i.name])),
    [integrations]
  );

  const sortedAlerts = useMemo(() => {
    return [...alerts].sort((a, b) => {
      const statusGap = statusWeight(b.status) - statusWeight(a.status);
      if (statusGap !== 0) return statusGap;
      const severityGap = severityWeight(b.severity) - severityWeight(a.severity);
      if (severityGap !== 0) return severityGap;
      const timeGap = new Date(b.triggeredAt).getTime() - new Date(a.triggeredAt).getTime();
      if (!Number.isNaN(timeGap) && timeGap !== 0) return timeGap;
      return b.id.localeCompare(a.id);
    });
  }, [alerts]);

  const filteredAlerts = useMemo(() => {
    const searchKey = deferredKeyword.trim().toLowerCase();
    return sortedAlerts.filter((alert) => {
      if (severityFilter !== "all" && alert.severity !== severityFilter) return false;
      if (statusFilter !== "all" && alert.status !== statusFilter) return false;
      if (!searchKey) return true;
      const candidate = `${alert.nodeName} ${alert.policyName} ${alert.errorCode} ${alert.message}`.toLowerCase();
      return candidate.includes(searchKey);
    });
  }, [deferredKeyword, severityFilter, sortedAlerts, statusFilter]);

  const refreshDeliveries = (alertId: string) => {
    setDeliveryLoadingAlertId(alertId);
    void fetchAlertDeliveries(alertId)
      .then((rows) => {
        setDeliveryMap((prev) => ({ ...prev, [alertId]: rows }));
      })
      .catch((error) => toast.error(getErrorMessage(error)))
      .finally(() => setDeliveryLoadingAlertId(null));
  };

  const toggleDeliveries = (alertId: string) => {
    if (deliveryOpenAlertId === alertId) {
      setDeliveryOpenAlertId(null);
      return;
    }
    setDeliveryOpenAlertId(alertId);
    if (!deliveryMap[alertId]) {
      refreshDeliveries(alertId);
    }
  };

  return (
    <Card className="border-border/75">
      <CardContent className="space-y-4 pt-6">
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex items-center gap-2 font-medium">
            {t("notifications.alertCenterTitle")}
          </div>
          <Button size="sm" variant="outline" onClick={resetFilters}>
            <RefreshCw className="mr-1 size-3.5" />
            {t("common.resetFilter")}
          </Button>
        </div>
        <FilterPanel sticky={false} className="grid gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-[2fr_1fr_1fr_auto] items-center">
          <SearchInput
            containerClassName="w-full"
            aria-label={t("notifications.keywordFilter")}
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
            placeholder={t("notifications.keywordPlaceholder")}
          />
          <AppSelect
            containerClassName="w-full"
            aria-label={t("notifications.severityFilter")}
            value={severityFilter}
            onChange={(event) => setSeverityFilter(event.target.value as typeof severityFilter)}
          >
            <option value="all">{t("notifications.allSeverities")}</option>
            <option value="critical">{t("status.alert.critical")}</option>
            <option value="warning">{t("status.alert.warning")}</option>
            <option value="info">{t("status.alert.info")}</option>
          </AppSelect>
          <AppSelect
            containerClassName="w-full"
            aria-label={t("notifications.statusFilter")}
            value={statusFilter}
            onChange={(event) => setStatusFilter(event.target.value as typeof statusFilter)}
          >
            <option value="all">{t("common.all")}{t("common.status")}</option>
            <option value="open">{t("notifications.statusOpen")}</option>
            <option value="acked">{t("notifications.statusAcked")}</option>
            <option value="resolved">{t("notifications.statusResolved")}</option>
          </AppSelect>
          <div className="flex items-center justify-end col-span-full sm:col-span-2 md:col-span-3 lg:col-span-1">
            <Button size="sm" variant="outline" onClick={resetFilters}>
              {t("common.reset")}
            </Button>
          </div>
        </FilterPanel>

        <FilterSummary filtered={filteredAlerts.length} total={alerts.length} unit={t("notifications.alertUnit")} />

        <div className="space-y-2">
          {filteredAlerts.length ? (
            filteredAlerts.map((alert) => {
              const severity = getSeverityMeta(alert.severity);
              const status = alertStatusMeta(alert.status);
              const isDeliveryOpen = deliveryOpenAlertId === alert.id;
              const deliveryPanelId = `alert-delivery-panel-${alert.id}`;
              return (
                <div key={alert.id} ref={alert.id === initialAlertId ? highlightRef : undefined} className="rounded-xl border border-border/75 bg-background/65 p-3 shadow-sm">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <StatusPulse tone={severityToTone(alert.severity)} />
                      <p className="font-medium">
                        {alert.nodeName} · {alert.taskId ? `${t("notifications.taskLabel", { id: alert.taskId })}${alert.taskRunId ? ` ${t("notifications.taskRunLabel", { id: alert.taskRunId })}` : ""}` : t("notifications.nodeProbe")}
                      </p>
                    </div>
                    <div className="flex items-center gap-1">
                      <Badge variant={severity.variant}>{severity.label}</Badge>
                      <Badge variant={status.variant}>{status.label}</Badge>
                    </div>
                  </div>

                  <p className="mt-2 text-sm">{alert.message}</p>
                  <div className="mt-1 flex flex-wrap gap-2 text-xs text-muted-foreground">
                    <span>{t("notifications.alertPolicy", { name: alert.policyName })}</span>
                    <span>{t("notifications.alertErrorCode", { code: alert.errorCode })}</span>
                    <span>{t("notifications.alertTriggeredAt", { time: alert.triggeredAt })}</span>
                  </div>

                  <div className="mt-3 flex flex-wrap items-center gap-2">
                    <Button
                      size="sm"
                      onClick={() => {
                        if (!alert.taskId) {
                          toast.error(t("notifications.noAlertTask"));
                          return;
                        }
                        void retryAlert(alert.id)
                          .then(() => toast.success(t("notifications.retryTriggered", { id: alert.taskId })))
                          .catch((error) => toast.error(getErrorMessage(error)));
                      }}
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
                          onClick={() => {
                            void acknowledgeAlert(alert.id)
                              .then(() => toast.success(t("notifications.ackSuccess", { code: alert.errorCode })))
                              .catch((error) => toast.error(getErrorMessage(error)));
                          }}
                        >
                          {t("notifications.markRead")}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          disabled={alert.status === "resolved"}
                          onClick={() => {
                            void resolveAlert(alert.id)
                              .then(() => toast.success(t("notifications.resolveSuccess", { code: alert.errorCode })))
                              .catch((error) => toast.error(getErrorMessage(error)));
                          }}
                        >
                          {t("notifications.markResolved")}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          onClick={() => toggleDeliveries(alert.id)}
                        >
                          {isDeliveryOpen ? t("notifications.collapseDelivery") : t("notifications.deliveryRecords")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>

                  {isDeliveryOpen ? (
                    <div
                      id={deliveryPanelId}
                      role="region"
                      aria-label={t("notifications.deliveryPanelAriaLabel", { code: alert.errorCode })}
                      aria-busy={deliveryLoadingAlertId === alert.id}
                      className="mt-3 rounded-md border border-border/70 bg-muted/25 p-2"
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
                              onClick={() => {
                                setRetryingAllAlertId(alert.id);
                                void retryFailedAlertDeliveries(alert.id)
                                  .then((result) => {
                                    toast.success(result.message);
                                    refreshDeliveries(alert.id);
                                  })
                                  .catch((error) => toast.error(getErrorMessage(error)))
                                  .finally(() => setRetryingAllAlertId(null));
                              }}
                            >
                              {retryingAllAlertId === alert.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                              {t("notifications.resendAllFailed")}
                            </Button>
                          ) : null}

                          {(deliveryMap[alert.id] ?? []).map((delivery) => (
                            <div key={delivery.id} className="rounded border border-border/70 bg-background/80 px-2 py-1.5 text-xs">
                              <div className="flex flex-wrap items-center justify-between gap-2">
                                <span className="font-medium">
                                  {integrationNameMap.get(delivery.integrationId) ?? delivery.integrationId}
                                </span>
                                <Badge variant={delivery.status === "sent" ? "success" : "danger"}>
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
                                  onClick={() => {
                                    const actionKey = `${alert.id}:${delivery.integrationId}`;
                                    setRetryingDeliveryKey(actionKey);
                                    void retryAlertDelivery(alert.id, delivery.integrationId)
                                      .then((result) => {
                                        toast.success(result.message);
                                        refreshDeliveries(alert.id);
                                      })
                                      .catch((error) => toast.error(getErrorMessage(error)))
                                      .finally(() => setRetryingDeliveryKey(null));
                                  }}
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
                  ) : null}
                </div>
              );
            })
          ) : !loading ? (
            <FilteredEmptyState
              icon={BellRing}
              title={t("notifications.emptyTitle")}
              description={t("notifications.emptyDesc")}
              onReset={resetFilters}
            />
          ) : null}
        </div>
      </CardContent>
    </Card>
  );
}
