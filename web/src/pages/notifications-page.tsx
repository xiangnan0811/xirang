import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { useSharedContext } from "@/context/shared-context";
import { useTasksContext } from "@/context/tasks-context";
import { useAlertsContext } from "@/context/alerts-context";
import { useIntegrationsContext } from "@/context/integrations-context";
import { DeliveryStatsCard } from "@/pages/notifications-page.delivery-stats";
import { AlertCenter } from "@/pages/notifications/alert-center";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";

export function NotificationsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { globalSearch, setGlobalSearch, refreshVersion } = useSharedContext();
  const { tasks, refreshTasks } = useTasksContext();
  const { fetchAlertDeliveryStats } = useAlertsContext();
  const { integrations, refreshIntegrations } = useIntegrationsContext();

  useEffect(() => {
    void refreshIntegrations();
    void refreshTasks();
  }, [refreshIntegrations, refreshTasks]);

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
  const [deliveryFailedCount, setDeliveryFailedCount] = useState(0);
  useEffect(() => {
    if (!token) return;
    fetchAlertDeliveryStats(24)
      .then((stats) => setDeliveryFailedCount(stats.totalFailed))
      .catch(() => {});
  }, [fetchAlertDeliveryStats, token, refreshVersion]);

  const activeIntegrations = integrations.filter((item) => item.enabled).length;
  const failedTasks = tasks.filter((task) => task.status === "failed").length;

  return (
    <div className="space-y-5 animate-fade-in">
      <StatCardsSection
        className="animate-slide-up [animation-delay:150ms]"
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
            value: failedTasks,
            description: t("notifications.statFailedTasks24hDesc"),
            tone: "info",
          },
          {
            title: "投递失败（24h）",
            value: deliveryFailedCount,
            description: "近 24 小时内失败的通知投递数",
            tone: deliveryFailedCount > 0 ? ("warning" as const) : ("success" as const),
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
