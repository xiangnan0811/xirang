import { useCallback, type Dispatch, type SetStateAction } from "react";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import { useApiAction } from "@/hooks/use-api-action";
import { buildDemoIntegration } from "@/hooks/use-console-data.demo";
import type {
  AlertBulkRetryResult,
  AlertDeliveryRecord,
  AlertDeliveryRetryResult,
  AlertDeliveryStats,
  AlertRecord,
  IntegrationChannel,
  IntegrationProbeResult,
  NewIntegrationInput
} from "@/types/domain";

type UseIntegrationAlertOperationsParams = {
  token: string | null;
  alerts: AlertRecord[];
  integrations: IntegrationChannel[];
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  setIntegrations: Dispatch<SetStateAction<IntegrationChannel[]>>;
  setWarning: Dispatch<SetStateAction<string | null>>;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
  retryTask: (taskID: number) => Promise<void>;
};

export function useIntegrationAlertOperations({
  token,
  alerts,
  integrations,
  setAlerts,
  setIntegrations,
  setWarning,
  ensureDemoWriteAllowed,
  handleWriteApiError,
  retryTask
}: UseIntegrationAlertOperationsParams) {
  const exec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError });

  const addIntegration = useCallback(async (input: NewIntegrationInput) => {
    const result = await exec(i18n.t("notifications.actions.addIntegration"), (t) => apiClient.createIntegration(t, input));
    if (result) {
      if (result.ok) {
        setIntegrations((prev) => [result.data, ...prev]);
      }
      return;
    }
    setIntegrations((prev) => [buildDemoIntegration(input), ...prev]);
  }, [exec, setIntegrations]);

  const removeIntegration = useCallback(async (integrationID: string) => {
    await exec(i18n.t("notifications.actions.removeIntegration"), (t) => apiClient.deleteIntegration(t, integrationID));
    setIntegrations((prev) => prev.filter((integration) => integration.id !== integrationID));
  }, [exec, setIntegrations]);

  const updateIntegration = useCallback(async (integrationID: string, patch: Partial<IntegrationChannel> & { secret?: string; skipEndpointHint?: boolean }) => {
    const current = integrations.find((item) => item.id === integrationID);
    if (!current) {
      throw new Error(i18n.t("notifications.actions.integrationNotFound"));
    }
    const merged: IntegrationChannel = { ...current, ...patch };

    if (!token) {
      ensureDemoWriteAllowed(i18n.t("notifications.actions.updateIntegration"));
      setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? merged : item)));
      return;
    }
    const updated = await apiClient.updateIntegration(token, integrationID, { ...merged, skipEndpointHint: patch.skipEndpointHint });
    setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? updated : item)));
  }, [token, ensureDemoWriteAllowed, integrations, setIntegrations]);

  const testIntegration = useCallback(async (integrationID: string): Promise<IntegrationProbeResult> => {
    const result = await exec(i18n.t("notifications.actions.testIntegration"), (t) => apiClient.testIntegration(t, integrationID));
    if (result) {
      return result.ok ? result.data : { ok: false, message: i18n.t("notifications.testFailed"), latencyMs: 0 };
    }
    return { ok: true, message: i18n.t("notifications.actions.testNoticeSent"), latencyMs: 0 };
  }, [exec]);

  const patchIntegration = useCallback(async (integrationID: string, patch: Record<string, unknown>) => {
    if (!token) {
      ensureDemoWriteAllowed(i18n.t("notifications.actions.updateIntegration"));
      // demo 模式：将 snake_case API 字段映射为 camelCase 前端字段
      const mapped: Partial<IntegrationChannel> = {};
      if ("enabled" in patch) mapped.enabled = patch.enabled as boolean;
      if ("name" in patch) mapped.name = patch.name as string;
      if ("fail_threshold" in patch) mapped.failThreshold = patch.fail_threshold as number;
      if ("cooldown_minutes" in patch) mapped.cooldownMinutes = patch.cooldown_minutes as number;
      if ("proxy_url" in patch) mapped.proxyUrl = patch.proxy_url as string;
      setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? { ...item, ...mapped } : item)));
      return;
    }
    const updated = await apiClient.patchIntegration(token, integrationID, patch);
    setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? updated : item)));
  }, [token, ensureDemoWriteAllowed, setIntegrations]);

  const toggleIntegration = useCallback(async (integrationID: string) => {
    const current = integrations.find((item) => item.id === integrationID);
    if (!current) {
      return;
    }
    await patchIntegration(integrationID, { enabled: !current.enabled });
  }, [integrations, patchIntegration]);

  const retryAlert = useCallback(
    async (alertID: string) => {
      const target = alerts.find((alert) => alert.id === alertID);
      if (!target) {
        return;
      }
      if (!target.taskId) {
        const message = i18n.t("notifications.retryNoTask");
        setWarning(message);
        throw new Error(message);
      }
      await retryTask(target.taskId);
    },
    [alerts, retryTask, setWarning]
  );

  const acknowledgeAlert = useCallback(async (alertID: string) => {
    const result = await exec(i18n.t("notifications.actions.ackAlert"), (t) => apiClient.ackAlert(t, alertID));
    if (result) {
      if (result.ok) {
        setAlerts((prev) => prev.map((alert) => (alert.id === alertID ? result.data : alert)));
      }
      return;
    }
    setAlerts((prev) =>
      prev.map((alert) =>
        alert.id === alertID
          ? { ...alert, status: alert.status === "open" ? "acked" : alert.status }
          : alert
      )
    );
  }, [exec, setAlerts]);

  const resolveAlert = useCallback(async (alertID: string) => {
    const result = await exec(i18n.t("notifications.actions.resolveAlert"), (t) => apiClient.resolveAlert(t, alertID));
    if (result) {
      if (result.ok) {
        setAlerts((prev) => prev.map((alert) => (alert.id === alertID ? result.data : alert)));
      }
      return;
    }
    setAlerts((prev) =>
      prev.map((alert) =>
        alert.id === alertID
          ? { ...alert, status: "resolved", retryable: false }
          : alert
      )
    );
  }, [exec, setAlerts]);

  const fetchAlertDeliveries = useCallback(async (alertID: string): Promise<AlertDeliveryRecord[]> => {
    if (token) {
      try {
        return await apiClient.getAlertDeliveries(token, alertID);
      } catch (error) {
        setWarning(getErrorMessage(error, i18n.t("notifications.deliveryLoadFailed")));
        return [];
      }
    }
    return [];
  }, [setWarning, token]);

  const fetchAlertDeliveryStats = useCallback(async (hours = 24): Promise<AlertDeliveryStats> => {
    const normalizedHours = Number.isFinite(hours) && hours > 0 ? Math.floor(hours) : 24;

    if (token) {
      try {
        return await apiClient.getAlertDeliveryStats(token, { hours: normalizedHours });
      } catch (error) {
        setWarning(getErrorMessage(error, i18n.t("notifications.deliveryStatsLoadFailed")));
      }
    }

    return {
      windowHours: normalizedHours,
      totalSent: 0,
      totalFailed: 0,
      successRate: 0,
      byIntegration: []
    };
  }, [setWarning, token]);

  const retryAlertDelivery = useCallback(async (alertID: string, integrationID: string): Promise<AlertDeliveryRetryResult> => {
    const result = await exec(i18n.t("notifications.actions.retryDelivery"), (t) => apiClient.retryAlertDelivery(t, alertID, integrationID));
    if (result) {
      if (result.ok) return result.data;
      return {
        ok: false,
        message: i18n.t("notifications.resendFailed"),
        delivery: {
          id: "",
          alertId: alertID,
          integrationId: integrationID,
          status: "failed",
          createdAt: "-"
        }
      };
    }
    return {
      ok: true,
      message: i18n.t("notifications.resendSubmitted"),
      delivery: {
        id: `delivery-${Date.now()}`,
        alertId: alertID,
        integrationId: integrationID,
        status: "sent",
        createdAt: new Date().toLocaleString("zh-CN", { hour12: false })
      }
    };
  }, [exec]);

  const retryFailedAlertDeliveries = useCallback(async (alertID: string): Promise<AlertBulkRetryResult> => {
    const result = await exec(i18n.t("notifications.actions.retryFailedDeliveries"), (t) => apiClient.retryFailedDeliveries(t, alertID));
    if (result) {
      if (result.ok) return result.data;
      return {
        ok: false,
        message: i18n.t("notifications.batchResendFailed"),
        totalFailed: 0,
        successCount: 0,
        failedCount: 0,
        newDeliveries: []
      };
    }
    return {
      ok: true,
      message: i18n.t("notifications.batchResendSubmitted"),
      totalFailed: 0,
      successCount: 0,
      failedCount: 0,
      newDeliveries: []
    };
  }, [exec]);

  return {
    addIntegration,
    removeIntegration,
    updateIntegration,
    patchIntegration,
    testIntegration,
    toggleIntegration,
    retryAlert,
    acknowledgeAlert,
    resolveAlert,
    fetchAlertDeliveries,
    fetchAlertDeliveryStats,
    retryAlertDelivery,
    retryFailedAlertDeliveries
  };
}
