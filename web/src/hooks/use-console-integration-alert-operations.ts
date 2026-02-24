import { useCallback, type Dispatch, type SetStateAction } from "react";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
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
import { createIntegrationId } from "@/hooks/use-console-data.utils";

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
  const addIntegration = useCallback(async (input: NewIntegrationInput) => {
    if (token) {
      try {
        const created = await apiClient.createIntegration(token, input);
        setIntegrations((prev) => [created, ...prev]);
        return;
      } catch (error) {
        handleWriteApiError("新增通知通道", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("新增通知通道");
    }

    const next: IntegrationChannel = {
      id: createIntegrationId(input.name || input.type),
      type: input.type,
      name: input.name,
      endpoint: input.endpoint,
      enabled: input.enabled,
      failThreshold: Math.max(1, input.failThreshold),
      cooldownMinutes: Math.max(1, input.cooldownMinutes)
    };
    setIntegrations((prev) => [next, ...prev]);
  }, [ensureDemoWriteAllowed, handleWriteApiError, setIntegrations, token]);

  const removeIntegration = useCallback(async (integrationID: string) => {
    if (token) {
      try {
        await apiClient.deleteIntegration(token, integrationID);
      } catch (error) {
        handleWriteApiError("删除通知通道", error);
      }
    } else {
      ensureDemoWriteAllowed("删除通知通道");
    }
    setIntegrations((prev) => prev.filter((integration) => integration.id !== integrationID));
  }, [ensureDemoWriteAllowed, handleWriteApiError, setIntegrations, token]);

  const updateIntegration = useCallback(async (integrationID: string, patch: Partial<IntegrationChannel>) => {
    const current = integrations.find((item) => item.id === integrationID);
    if (!current) {
      return;
    }

    const merged: IntegrationChannel = {
      ...current,
      ...patch
    };

    if (token) {
      try {
        const updated = await apiClient.updateIntegration(token, integrationID, merged);
        setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? updated : item)));
        return;
      } catch (error) {
        handleWriteApiError("更新通知通道", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("更新通知通道");
    }

    setIntegrations((prev) => prev.map((item) => (item.id === integrationID ? merged : item)));
  }, [ensureDemoWriteAllowed, handleWriteApiError, integrations, setIntegrations, token]);

  const testIntegration = useCallback(async (integrationID: string): Promise<IntegrationProbeResult> => {
    if (token) {
      try {
        return await apiClient.testIntegration(token, integrationID);
      } catch (error) {
        handleWriteApiError("测试通知通道", error);
        return { ok: false, message: "测试失败", latencyMs: 0 };
      }
    } else {
      ensureDemoWriteAllowed("测试通知通道");
    }
    return {
      ok: true,
      message: "测试通知已发送",
      latencyMs: 0
    };
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const toggleIntegration = useCallback(async (integrationID: string) => {
    const current = integrations.find((item) => item.id === integrationID);
    if (!current) {
      return;
    }
    await updateIntegration(integrationID, {
      enabled: !current.enabled
    });
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
    if (token) {
      try {
        const updated = await apiClient.ackAlert(token, alertID);
        setAlerts((prev) => prev.map((alert) => (alert.id === alertID ? updated : alert)));
        return;
      } catch (error) {
        handleWriteApiError("确认告警", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("确认告警");
    }

    setAlerts((prev) =>
      prev.map((alert) =>
        alert.id === alertID
          ? {
              ...alert,
              status: alert.status === "open" ? "acked" : alert.status
            }
          : alert
      )
    );
  }, [ensureDemoWriteAllowed, handleWriteApiError, setAlerts, token]);

  const resolveAlert = useCallback(async (alertID: string) => {
    if (token) {
      try {
        const updated = await apiClient.resolveAlert(token, alertID);
        setAlerts((prev) => prev.map((alert) => (alert.id === alertID ? updated : alert)));
        return;
      } catch (error) {
        handleWriteApiError("恢复告警", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("恢复告警");
    }

    setAlerts((prev) =>
      prev.map((alert) =>
        alert.id === alertID
          ? {
              ...alert,
              status: "resolved",
              retryable: false
            }
          : alert
      )
    );
  }, [ensureDemoWriteAllowed, handleWriteApiError, setAlerts, token]);

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
        return await apiClient.getAlertDeliveryStats(token, {
          hours: normalizedHours
        });
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
    if (token) {
      try {
        return await apiClient.retryAlertDelivery(token, alertID, integrationID);
      } catch (error) {
        handleWriteApiError("重发通知", error);
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
    } else {
      ensureDemoWriteAllowed("重发通知");
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
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const retryFailedAlertDeliveries = useCallback(async (alertID: string): Promise<AlertBulkRetryResult> => {
    if (token) {
      try {
        return await apiClient.retryFailedDeliveries(token, alertID);
      } catch (error) {
        handleWriteApiError("批量重发通知", error);
        return {
          ok: false,
          message: "批量重发失败",
          totalFailed: 0,
          successCount: 0,
          failedCount: 0,
          newDeliveries: []
        };
      }
    } else {
      ensureDemoWriteAllowed("批量重发通知");
    }
    return {
      ok: true,
      message: "批量重发已提交",
      totalFailed: 0,
      successCount: 0,
      failedCount: 0,
      newDeliveries: []
    };
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

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
