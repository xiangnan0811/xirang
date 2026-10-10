import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link, useSearchParams } from "react-router-dom";
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

type AlertUnreadStats = { total: number; critical: number; warning: number };

export function NotificationsPage() {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const canWriteAlerts = role === "admin" || role === "operator";
  const canTriggerTasks = role === "admin" || role === "operator";
  const canRetryDelivery = role === "admin";
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

  // 未读统计绑定当前请求代次。确认、解决、刷新、换身份或卸载都会让旧响应失效。
  const [alertStatsVersion, setAlertStatsVersion] = useState(0);
  const alertStatsRequestKey = useMemo(
    () => ({ token, refreshVersion, alertStatsVersion }),
    [token, refreshVersion, alertStatsVersion],
  );
  const [alertStats, setAlertStats] = useState<
    | { key: typeof alertStatsRequestKey; status: "success"; data: AlertUnreadStats }
    | { key: typeof alertStatsRequestKey; status: "error" }
    | null
  >(null);
  const refreshAlertStats = useCallback(() => {
    setAlertStatsVersion((version) => version + 1);
  }, []);
  useEffect(() => {
    const key = alertStatsRequestKey;
    if (!key.token) {
      return;
    }
    const controller = new AbortController();
    let active = true;
    apiClient
      .getAlertUnreadCount(key.token, { signal: controller.signal })
      .then((data) => {
        if (!active || controller.signal.aborted) {
          return;
        }
        setAlertStats({ key, status: "success", data });
      })
      .catch(() => {
        if (!active || controller.signal.aborted) {
          return;
        }
        setAlertStats({ key, status: "error" });
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [alertStatsRequestKey]);

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

  let alertStatsPhase: "unavailable" | "loading" | "success" | "error";
  let alertStatsValue: AlertUnreadStats | null = null;
  if (!token) {
    alertStatsPhase = "unavailable";
  } else if (!alertStats || alertStats.key !== alertStatsRequestKey) {
    alertStatsPhase = "loading";
  } else if (alertStats.status === "error") {
    alertStatsPhase = "error";
  } else {
    alertStatsPhase = "success";
    alertStatsValue = alertStats.data;
  }

  const alertStatsDescription = (successText: string, withRetry: boolean): ReactNode => {
    if (alertStatsPhase === "success") {
      return <span className="block max-w-full break-words">{successText}</span>;
    }
    if (alertStatsPhase === "error") {
      return (
        <span className="flex min-w-0 max-w-full flex-col items-start gap-2">
          <span className="max-w-full break-words">{t("notifications.alertStatsLoadFailed")}</span>
          {withRetry ? (
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="max-w-full"
              aria-label={t("notifications.alertStatsRetry")}
              onClick={refreshAlertStats}
            >
              {t("common.retry")}
            </Button>
          ) : null}
        </span>
      );
    }
    if (alertStatsPhase === "loading") {
      return t("common.loading");
    }
    return (
      <span className="block max-w-full break-words">{t("notifications.alertStatsUnavailable")}</span>
    );
  };
  const alertStatsHeroText =
    alertStatsPhase === "success" && alertStatsValue
      ? t("notifications.pendingAlertsMeta", { count: alertStatsValue.total })
      : t(
          alertStatsPhase === "error"
            ? "notifications.alertStatsLoadFailed"
            : alertStatsPhase === "unavailable"
              ? "notifications.alertStatsUnavailable"
              : "common.loading",
        );

  const activeIntegrations = integrations.filter((item) => item.enabled).length;

  return (
    <div className="space-y-5 animate-fade-in">
      <PageHero
        title={t("notifications.pageTitle")}
        subtitle={t("notifications.pageDesc")}
        meta={
          <>
            <Badge
              tone={
                alertStatsPhase === "success" && alertStatsValue && alertStatsValue.total > 0
                  ? "destructive"
                  : alertStatsPhase === "error"
                    ? "warning"
                    : alertStatsPhase === "success"
                      ? "success"
                      : "neutral"
              }
            >
              {alertStatsHeroText}
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

      {role === "admin" ? (
        <nav aria-label={t("notifications.configNavLabel")} className="flex flex-wrap items-center gap-2">
          <Button asChild variant="outline" size="sm">
            <Link to="/app/settings?tab=channels">{t("notifications.configChannels")}</Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link to="/app/settings?tab=silences">{t("notifications.configSilences")}</Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link to="/app/settings?tab=escalation">{t("notifications.configEscalation")}</Link>
          </Button>
        </nav>
      ) : null}

      <StatCardsSection
        className="animate-slide-up [animation-delay:150ms]"
        compact
        items={[
          {
            title: t("notifications.statOpenAlerts"),
            value: alertStatsValue ? alertStatsValue.total : "—",
            description: alertStatsDescription(t("notifications.statOpenAlertsDesc"), true),
            tone:
              alertStatsPhase === "success"
                ? "destructive"
                : alertStatsPhase === "error"
                  ? "warning"
                  : undefined,
          },
          {
            title: t("notifications.statCriticalAlerts"),
            value: alertStatsValue ? alertStatsValue.critical : "—",
            description: alertStatsDescription(t("notifications.statCriticalAlertsDesc"), false),
            tone:
              alertStatsPhase === "success" || alertStatsPhase === "error" ? "warning" : undefined,
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
          canWriteAlerts={canWriteAlerts}
          canTriggerTasks={canTriggerTasks}
          canRetryDelivery={canRetryDelivery}
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
