import { useCallback, type Dispatch, type SetStateAction } from "react";
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
    const result = await exec("新增通知通道", (t) => apiClient.createIntegration(t, input));
    if (result) {
      if (result.ok) {
        setIntegrations((prev) => [result.data, ...prev]);
      }
      return;
    }
    setIntegrations((prev) => [buildDemoIntegration(input), ...prev]);
  }, [exec, setIntegrations]);

  const removeIntegration = useCallback(async (integrationID: string) => {
    await exec("删除通知通道", (t) => apiClient.deleteIntegration(t, integrationID));
    setIntegrations((prev) => prev.filter((integration) => integration.id !== integrationID));
  }, [exec, setIntegrations]);

  const updateIntegration = useCallback(async (integrationID: string, patch: Partial<IntegrationChannel> & { secret?: string }) => {
    const current = integrations.find((item) => item.id === integrationID);
    if (!current) {
      throw new Error(`通知方式不存在或已被删除，请刷新后重试。`);
    }
    const merged: IntegrationChannel = { ...current, ...patch };

    const result = await exec("更新通知通道", (t) => apiClient.updateIntegration(t, integrationID, merged));
    if (result) {
      if (result.ok) {
        setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? result.data : item)));
      }
      return;
    }
    setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? merged : item)));
  }, [exec, integrations, setIntegrations]);

  const testIntegration = useCallback(async (integrationID: string): Promise<IntegrationProbeResult> => {
    const result = await exec("测试通知通道", (t) => apiClient.testIntegration(t, integrationID));
    if (result) {
      return result.ok ? result.data : { ok: false, message: "测试失败", latencyMs: 0 };
    }
    return { ok: true, message: "测试通知已发送", latencyMs: 0 };
  }, [exec]);

  const toggleIntegration = useCallback(async (integrationID: string) => {
    const current = integrations.find((item) => item.id === integrationID);
    if (!current) {
      return;
    }
    await updateIntegration(integrationID, { enabled: !current.enabled });
  }, [integrations, updateIntegration]);

  const retryAlert = useCallback(
    async (alertID: string) => {
      const target = alerts.find((alert) => alert.id === alertID);
      if (!target) {
        return;
      }
      if (!target.taskId) {
        const message = "当前告警未绑定任务，无法重试。请先修复节点连接问题。";
        setWarning(message);
        throw new Error(message);
      }
      await retryTask(target.taskId);
    },
    [alerts, retryTask, setWarning]
  );

  const acknowledgeAlert = useCallback(async (alertID: string) => {
    const result = await exec("确认告警", (t) => apiClient.ackAlert(t, alertID));
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
    const result = await exec("恢复告警", (t) => apiClient.resolveAlert(t, alertID));
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
        setWarning(getErrorMessage(error, "获取告警投递记录失败"));
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
        setWarning(getErrorMessage(error, "获取告警投递统计失败"));
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
    const result = await exec("重发通知", (t) => apiClient.retryAlertDelivery(t, alertID, integrationID));
    if (result) {
      if (result.ok) return result.data;
      return {
        ok: false,
        message: "重发失败",
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
      message: "通知重发已提交",
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
    const result = await exec("批量重发通知", (t) => apiClient.retryFailedDeliveries(t, alertID));
    if (result) {
      if (result.ok) return result.data;
      return {
        ok: false,
        message: "批量重发失败",
        totalFailed: 0,
        successCount: 0,
        failedCount: 0,
        newDeliveries: []
      };
    }
    return {
      ok: true,
      message: "批量重发已提交",
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
