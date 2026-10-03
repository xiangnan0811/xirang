import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { useSharedContext } from "@/context/shared-context.hooks";
import { useAlertsContext } from "@/context/alerts-context.hooks";
import { useIntegrationsContext } from "@/context/integrations-context.hooks";
import { DeliveryStatsCard } from "@/pages/notifications-page.delivery-stats";
import { AlertCenter } from "@/pages/notifications/alert-center";
import { PageHero } from "@/components/ui/page-hero";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { useAuth } from "@/context/auth-context.hooks";
import { apiClient } from "@/lib/api/client";
import type { TaskFailureSummary } from "@/lib/api/tasks-api";

export function NotificationsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { globalSearch, setGlobalSearch, refreshVersion } = useSharedContext();
  const { fetchAlertDeliveryStats } = useAlertsContext();
  const { integrations, refreshIntegrations } = useIntegrationsContext();

  useEffect(() => {
    void refreshIntegrations();
  }, [refreshIntegrations]);

  const [searchParams, setSearchParams] = useSearchParams();
  const highlightAlertId = searchParams.get("alert");
  const clearHighlightAlert = useCallback(() => {
    if (searchParams.has("alert")) {
      searchParams.delete("alert");
      setSearchParams(searchParams, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  // 统计卡片数据：复用 getAlertUnreadCount API
  const [alertStats, setAlertStats] = useState({ total: 0, critical: 0, warning: 0 });
  const refreshAlertStats = useCallback(() => {
    if (!token) return;
    apiClient.getAlertUnreadCount(token).then(setAlertStats).catch(() => {});
  }, [token]);
  useEffect(() => {
    refreshAlertStats();
  }, [refreshAlertStats, refreshVersion]);

  // 投递重试统计
  const [deliveryFailedCount, setDeliveryFailedCount] = useState<number | null>(null);
  const [deliveryStatsError, setDeliveryStatsError] = useState(false);
  useEffect(() => {
    /* eslint-disable react-hooks/set-state-in-effect */
    setDeliveryFailedCount(null);
    setDeliveryStatsError(false);
    /* eslint-enable react-hooks/set-state-in-effect */
    if (!token) {
      return;
    }
    let active = true;
    fetchAlertDeliveryStats(24)
      .then((stats) => {
        if (!active) {
          return;
        }
        setDeliveryFailedCount(stats.totalFailed);
      })
      .catch(() => {
        if (!active) {
          return;
        }
        setDeliveryStatsError(true);
      });
    return () => {
      active = false;
    };
  }, [fetchAlertDeliveryStats, token, refreshVersion]);

  const [retryVersion, setRetryVersion] = useState(0);
  const requestKey = useMemo(
    () => ({ token, refreshVersion, retryVersion }),
    [token, refreshVersion, retryVersion],
  );
  const [failureSummary, setFailureSummary] = useState<
    | { key: typeof requestKey; status: "success"; data: TaskFailureSummary }
    | { key: typeof requestKey; status: "error" }
    | null
  >(null);

  useEffect(() => {
    const key = requestKey;
    if (!key.token) {
      return;
    }
    const controller = new AbortController();
    let active = true;
    apiClient
      .getTaskFailureSummary(key.token, { signal: controller.signal })
      .then((data) => {
        if (!active || controller.signal.aborted) {
          return;
        }
        setFailureSummary({ key, status: "success", data });
      })
      .catch(() => {
        if (!active || controller.signal.aborted) {
          return;
        }
        setFailureSummary({ key, status: "error" });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [requestKey]);

  let failureSummaryPhase: "unavailable" | "loading" | "success" | "error";
  let failureSummaryValue: number | "—" = "—";
  if (!token) {
    failureSummaryPhase = "unavailable";
  } else if (!failureSummary || failureSummary.key !== requestKey) {
    failureSummaryPhase = "loading";
  } else if (failureSummary.status === "error") {
    failureSummaryPhase = "error";
  } else {
    failureSummaryPhase = "success";
    failureSummaryValue = failureSummary.data.failedTasks;
  }
  let failureSummaryDescription: ReactNode;
  if (failureSummaryPhase === "success") {
    failureSummaryDescription = (
      <span className="block max-w-full break-words">{t("notifications.statFailedTasks24hDesc")}</span>
    );
  } else if (failureSummaryPhase === "error") {
    failureSummaryDescription = (
      <span className="flex min-w-0 max-w-full flex-col items-start gap-2">
        <span className="max-w-full break-words">{t("notifications.taskFailureStatsLoadFailed")}</span>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="max-w-full"
          aria-label={t("notifications.taskFailureStatsRetry")}
          onClick={() => setRetryVersion((version) => version + 1)}
        >
          {t("common.retry")}
        </Button>
      </span>
    );
  } else if (failureSummaryPhase === "loading") {
    failureSummaryDescription = t("common.loading");
  } else {
    failureSummaryDescription = (
      <span className="block max-w-full break-words">{t("notifications.taskFailureStatsUnavailable")}</span>
    );
  }

  const activeIntegrations = integrations.filter((item) => item.enabled).length;

  return (
    <div className="space-y-5 animate-fade-in">
      <PageHero
        title={t("notifications.pageTitle")}
        subtitle={t("notifications.pageDesc")}
        meta={
          <>
            <Badge tone={alertStats.total > 0 ? "destructive" : "success"}>
              {t("notifications.pendingAlertsMeta", { count: alertStats.total })}
            </Badge>
            <Badge tone={activeIntegrations > 0 ? "success" : "warning"}>
              {t("notifications.channelsMeta", {
                active: activeIntegrations,
                total: integrations.length || 0,
              })}
            </Badge>
            <Badge
              tone={
                deliveryFailedCount === null
                  ? deliveryStatsError
                    ? "warning"
                    : "neutral"
                  : deliveryFailedCount > 0
                    ? "warning"
                    : "neutral"
              }
            >
              {deliveryFailedCount === null
                ? t(deliveryStatsError ? "notifications.deliveryStatsLoadFailed" : "common.loading")
                : t("notifications.deliveryFailedMeta", {
                    count: deliveryFailedCount,
                  })}
            </Badge>
          </>
        }
      />

      <StatCardsSection
        className="animate-slide-up [animation-delay:150ms]"
        compact
        items={[
          {
            title: t("notifications.statOpenAlerts"),
            value: alertStats.total,
            description: t("notifications.statOpenAlertsDesc"),
            tone: "destructive",
          },
          {
            title: t("notifications.statCriticalAlerts"),
            value: alertStats.critical,
            description: t("notifications.statCriticalAlertsDesc"),
            tone: "warning",
          },
          {
            title: t("notifications.statEnabledChannels"),
            value: `${activeIntegrations}/${integrations.length || 0}`,
            description: t("notifications.statEnabledChannelsDesc"),
            tone: "success",
          },
          {
            title: t("notifications.statFailedTasks24h"),
            value: failureSummaryValue,
            description: failureSummaryDescription,
            tone: failureSummaryPhase === "error" ? "warning" : failureSummaryPhase === "success" ? "info" : undefined,
          },
          {
            title: t("notifications.statDeliveryFailed24h"),
            value: deliveryFailedCount === null ? "—" : deliveryFailedCount,
            description:
              deliveryFailedCount === null
                ? t(deliveryStatsError ? "notifications.deliveryStatsLoadFailed" : "common.loading")
                : t("notifications.statDeliveryFailed24hDesc"),
            tone:
              deliveryFailedCount === null
                ? deliveryStatsError
                  ? ("warning" as const)
                  : undefined
                : deliveryFailedCount > 0
                  ? ("warning" as const)
                  : ("success" as const),
          },
        ]}
      />

      <DeliveryStatsCard fetchAlertDeliveryStats={fetchAlertDeliveryStats} />

      {token ? (
        <AlertCenter
          token={token}
          integrations={integrations}
          globalSearch={globalSearch}
          setGlobalSearch={setGlobalSearch}
          initialAlertId={highlightAlertId}
          onAlertHighlighted={clearHighlightAlert}
          onAlertMutated={refreshAlertStats}
          refreshVersion={refreshVersion}
        />
      ) : null}
    </div>
  );
}
