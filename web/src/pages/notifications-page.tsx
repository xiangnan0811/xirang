import React, { Suspense, useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { Plus } from "lucide-react";
import { useSharedContext } from "@/context/shared-context";
import { useTasksContext } from "@/context/tasks-context";
import { useAlertsContext } from "@/context/alerts-context";
import { useIntegrationsContext } from "@/context/integrations-context";
import { DeliveryStatsCard } from "@/pages/notifications-page.delivery-stats";
import { AlertCenter } from "@/pages/notifications/alert-center";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { PageHero } from "@/components/ui/page-hero";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { toast } from "@/components/ui/toast";

const IntegrationCreateDialog = React.lazy(() =>
  import("@/components/integration-create-dialog").then((m) => ({ default: m.IntegrationCreateDialog }))
);

export function NotificationsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { globalSearch, setGlobalSearch, refreshVersion } = useSharedContext();
  const { tasks, refreshTasks } = useTasksContext();
  const { fetchAlertDeliveryStats } = useAlertsContext();
  const { integrations, refreshIntegrations, addIntegration } = useIntegrationsContext();

  const [createDialogOpen, setCreateDialogOpen] = useState(false);

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

  const activeIntegrations = integrations.filter((item) => item.enabled).length;
  const failedTasks = tasks.filter((task) => task.status === "failed").length;

  return (
    <div className="space-y-5 animate-fade-in">
      <PageHero
        title={t("notifications.pageTitle")}
        subtitle={t("notifications.pageSubtitle", { total: alertStats.total, active: activeIntegrations })}
        actions={
          <Button shape="pill" onClick={() => setCreateDialogOpen(true)}>
            <Plus className="size-4" aria-hidden="true" />
            {t("notifications.addIntegration")}
          </Button>
        }
      />

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

      <Suspense fallback={null}>
        <IntegrationCreateDialog
          open={createDialogOpen}
          onOpenChange={setCreateDialogOpen}
          onSave={async (input) => {
            await addIntegration(input);
            setCreateDialogOpen(false);
            void refreshIntegrations();
            toast.success(t("notifications.integrationCreated"));
          }}
        />
      </Suspense>
    </div>
  );
}
