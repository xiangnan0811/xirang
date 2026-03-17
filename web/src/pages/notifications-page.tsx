import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOutletContext, useSearchParams } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { IntegrationCreateDialog } from "@/components/integration-create-dialog";
import { IntegrationEditorDialog, type IntegrationEditorDraft } from "@/components/integration-editor-dialog";
import { DeliveryStatsCard } from "@/pages/notifications-page.delivery-stats";
import { IntegrationManager } from "@/pages/notifications-page.integration-manager";
import { AlertCenter } from "@/pages/notifications-page.alert-center";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { toast } from "@/components/ui/toast";
import type { IntegrationChannel } from "@/types/domain";

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
    addIntegration,
    removeIntegration,
    toggleIntegration,
    updateIntegration,
    testIntegration,
    fetchAlertDeliveryStats,
    refreshIntegrations,
  } = useOutletContext<ConsoleOutletContext>();

  useEffect(() => {
    void refreshIntegrations();
  }, [refreshIntegrations]);

  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const [editDialogOpen, setEditDialogOpen] = useState(false);
  const [editingIntegration, setEditingIntegration] = useState<IntegrationChannel | null>(null);

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

  const openEditDialog = (integration: IntegrationChannel) => {
    setEditingIntegration(integration);
    setEditDialogOpen(true);
  };

  const handleRemoveIntegration = useCallback(async (id: string) => {
    await removeIntegration(id);
    setEditingIntegration((prev) => {
      if (prev?.id === id) {
        setEditDialogOpen(false);
        return null;
      }
      return prev;
    });
  }, [removeIntegration]);

  const handleEditIntegration = async (draft: IntegrationEditorDraft) => {
    await updateIntegration(draft.id, {
      name: draft.name,
      endpoint: draft.endpoint,
      failThreshold: draft.failThreshold,
      cooldownMinutes: draft.cooldownMinutes,
      secret: draft.secret || undefined,
      skipEndpointHint: draft.skipEndpointHint,
    });
    toast.success(t("notifications.integrationSaved", { name: draft.name }));
    setEditDialogOpen(false);
    setEditingIntegration(null);
  };

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

      <section className="grid gap-4 lg:grid-cols-[1.05fr_1.45fr]">
        <IntegrationManager
          integrations={integrations}
          toggleIntegration={toggleIntegration}
          testIntegration={testIntegration}
          removeIntegration={handleRemoveIntegration}
          onOpenCreate={() => setCreateDialogOpen(true)}
          onOpenEdit={openEditDialog}
        />

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
      </section>

      <IntegrationCreateDialog
        open={createDialogOpen}
        onOpenChange={setCreateDialogOpen}
        onSave={async (input) => {
          await addIntegration(input);
          setCreateDialogOpen(false);
          toast.success(t("notifications.integrationCreated"));
        }}
      />

      <IntegrationEditorDialog
        open={editDialogOpen}
        onOpenChange={(next) => {
          setEditDialogOpen(next);
          if (!next) {
            setEditingIntegration(null);
          }
        }}
        integration={editingIntegration}
        onSave={handleEditIntegration}
      />
    </div>
  );
}
