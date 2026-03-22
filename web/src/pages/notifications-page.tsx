import { useCallback, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { useOutletContext, useSearchParams } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { DeliveryStatsCard } from "@/pages/notifications-page.delivery-stats";
import { AlertCenter } from "@/pages/notifications-page.alert-center";
import { StatCardsSection } from "@/components/ui/stat-cards-section";

export function NotificationsPage() {
  const { t } = useTranslation();
  const {
    alerts,
    integrations,
    tasks,
    loading,
    globalSearch,
    setGlobalSearch,
    retryAlert,
    acknowledgeAlert,
    resolveAlert,
    fetchAlertDeliveries,
    retryAlertDelivery,
    retryFailedAlertDeliveries,
    fetchAlertDeliveryStats,
    refreshIntegrations,
    refreshTasks,
  } = useOutletContext<ConsoleOutletContext>();

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

  const activeIntegrations = integrations.filter((item) => item.enabled).length;
  const openAlerts = alerts.filter((item) => item.status === "open");
  const criticalAlerts = openAlerts.filter((item) => item.severity === "critical");
  const failedTasks = tasks.filter((task) => task.status === "failed").length;

  return (
    <div className="space-y-5 animate-fade-in">
      <StatCardsSection
        className="animate-slide-up [animation-delay:150ms]"
        items={[
          {
            title: t("notifications.statOpenAlerts"),
            value: openAlerts.length,
            description: t("notifications.statOpenAlertsDesc"),
            tone: "destructive",
          },
          {
            title: t("notifications.statCriticalAlerts"),
            value: criticalAlerts.length,
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
        ]}
      />

      <DeliveryStatsCard fetchAlertDeliveryStats={fetchAlertDeliveryStats} />

      <AlertCenter
        alerts={alerts}
        integrations={integrations}
        loading={loading}
        globalSearch={globalSearch}
        setGlobalSearch={setGlobalSearch}
        retryAlert={retryAlert}
        acknowledgeAlert={acknowledgeAlert}
        resolveAlert={resolveAlert}
        fetchAlertDeliveries={fetchAlertDeliveries}
        retryAlertDelivery={retryAlertDelivery}
        retryFailedAlertDeliveries={retryFailedAlertDeliveries}
        initialAlertId={highlightAlertId}
        onAlertHighlighted={clearHighlightAlert}
      />
    </div>
  );
}
