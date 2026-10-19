import { useCallback, type Dispatch, type SetStateAction } from "react";
import { useAuth } from "@/context/auth-context.hooks";
import i18n from "@/i18n";
import { assertSensitiveStepUpReady } from "@/lib/sensitive-step-up";
import { apiClient } from "@/lib/api/client";
import { formatTime } from "@/lib/api/core";
import { getErrorMessage } from "@/lib/utils";
import { useApiAction } from "@/hooks/use-api-action";
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
  retryTask: (taskID: number, isCurrent?: () => boolean) => Promise<void>;
  /** 渠道配置操作所属身份仍有效（真实 token 还要求 admin）；告警操作不使用此判定。 */
  isCurrentIntegrationOwner: () => boolean;
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
  retryTask,
  isCurrentIntegrationOwner
}: UseIntegrationAlertOperationsParams) {
  const { totpEnabled } = useAuth();
  const exec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError });

  const assertIntegrationOwner = useCallback(() => {
    if (!isCurrentIntegrationOwner()) {
      throw new DOMException("Operation belongs to an inactive session", "AbortError");
    }
  }, [isCurrentIntegrationOwner]);

  // 渠道写失败只在所属身份仍有效时进入共享 warning；迟到失败直接中止。
  const handleIntegrationWriteError = useCallback((action: string, error: unknown) => {
    assertIntegrationOwner();
    handleWriteApiError(action, error);
  }, [assertIntegrationOwner, handleWriteApiError]);
  const channelExec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError: handleIntegrationWriteError });

  const addIntegration = useCallback(async (input: NewIntegrationInput) => {
    assertIntegrationOwner();
    const result = await channelExec(i18n.t("notifications.actions.addIntegration"), (t) => apiClient.createIntegration(t, input));
    assertIntegrationOwner();
    if (result) {
      if (result.ok) {
        setIntegrations((prev) => [result.data, ...prev]);
      }
      return;
    }
    // Demo builders must not enter the production startup chunk (same as mock.ts).
    const { buildDemoIntegration } = await import("@/hooks/use-console-data.demo");
    assertIntegrationOwner();
    setIntegrations((prev) => [buildDemoIntegration(input), ...prev]);
  }, [assertIntegrationOwner, channelExec, setIntegrations]);

  const removeIntegration = useCallback(async (integrationID: string) => {
    assertIntegrationOwner();
    const label = i18n.t("notifications.actions.removeIntegration");
    const result = await channelExec(label, (t) => apiClient.deleteIntegration(t, integrationID));
    assertIntegrationOwner();
    if (result && !result.ok) {
      // 失败已由 warning 处理；不能把失败当作已删除。
      throw new Error(i18n.t("console.actionRequestFailed", { action: label }));
    }
    setIntegrations((prev) => prev.filter((integration) => integration.id !== integrationID));
  }, [assertIntegrationOwner, channelExec, setIntegrations]);

  const updateIntegration = useCallback(async (integrationID: string, patch: Partial<IntegrationChannel> & { secret?: string; skipEndpointHint?: boolean }) => {
    assertIntegrationOwner();
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
    let updated: IntegrationChannel;
    try {
      updated = await apiClient.updateIntegration(token, integrationID, { ...merged, skipEndpointHint: patch.skipEndpointHint });
    } catch (error) {
      assertIntegrationOwner();
      throw error;
    }
    assertIntegrationOwner();
    setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? updated : item)));
  }, [assertIntegrationOwner, token, ensureDemoWriteAllowed, integrations, setIntegrations]);

  const testIntegration = useCallback(async (integrationID: string): Promise<IntegrationProbeResult> => {
    assertIntegrationOwner();
    const result = await channelExec(i18n.t("notifications.actions.testIntegration"), (t) => apiClient.testIntegration(t, integrationID));
    assertIntegrationOwner();
    if (result) {
      return result.ok ? result.data : { ok: false, message: i18n.t("notifications.testFailed"), latencyMs: 0 };
    }
    return { ok: true, message: i18n.t("notifications.actions.testNoticeSent"), latencyMs: 0 };
  }, [assertIntegrationOwner, channelExec]);

  const patchIntegration = useCallback(async (integrationID: string, patch: Record<string, unknown>) => {
    assertIntegrationOwner();
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
    let updated: IntegrationChannel;
    try {
      updated = await apiClient.patchIntegration(token, integrationID, patch);
    } catch (error) {
      assertIntegrationOwner();
      throw error;
    }
    assertIntegrationOwner();
    setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? updated : item)));
  }, [assertIntegrationOwner, token, ensureDemoWriteAllowed, setIntegrations]);

  const toggleIntegration = useCallback(async (integrationID: string) => {
    assertIntegrationOwner();
    const current = integrations.find((item) => item.id === integrationID);
    if (!current) {
      return;
    }
    await patchIntegration(integrationID, { enabled: !current.enabled });
    assertIntegrationOwner();
  }, [assertIntegrationOwner, integrations, patchIntegration]);

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
      if (token) {
        assertSensitiveStepUpReady({ token, totpEnabled });
      }
      await retryTask(target.taskId);
    },
    [alerts, retryTask, setWarning, token, totpEnabled]
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
      return apiClient.getAlertDeliveryStats(token, { hours: normalizedHours });
    }

    return {
      windowHours: normalizedHours,
      totalSent: 0,
      totalFailed: 0,
      successRate: 0,
      byIntegration: []
    };
  }, [token]);

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
        createdAt: formatTime(new Date().toISOString())
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
