import { useCallback, useRef, type Dispatch, type SetStateAction } from "react";
import { apiClient } from "@/lib/api/client";
import { useIntegrationAlertOperations } from "@/hooks/use-console-integration-alert-operations";
import type {
  AlertRecord,
  IntegrationChannel,
} from "@/types/domain";

export type UseAlertsIntegrationsDomainParams = {
  token: string | null;
  alerts: AlertRecord[];
  integrations: IntegrationChannel[];
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  setIntegrations: Dispatch<SetStateAction<IntegrationChannel[]>>;
  setWarning: Dispatch<SetStateAction<string | null>>;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
  retryTask: (taskId: number) => Promise<void>;
};

// 告警 + 集成域：自持 refreshIntegrations 与 abort 控制，
// 接线 useIntegrationAlertOperations（与 retryTask 强耦合，故合并为一域）。
export function useAlertsIntegrationsDomain({
  token,
  alerts,
  integrations,
  setAlerts,
  setIntegrations,
  setWarning,
  ensureDemoWriteAllowed,
  handleWriteApiError,
  retryTask,
}: UseAlertsIntegrationsDomainParams) {
  const refreshIntegrationsAbortRef = useRef<AbortController | null>(null);

  const refreshIntegrations = useCallback(async () => {
    if (!token) return;
    refreshIntegrationsAbortRef.current?.abort();
    const controller = new AbortController();
    refreshIntegrationsAbortRef.current = controller;
    try {
      const result = await apiClient.getIntegrations(token);
      setIntegrations(result);
    } catch {
      // 按需刷新失败时静默处理
    }
  }, [token, setIntegrations]);

  const {
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
    retryFailedAlertDeliveries,
  } = useIntegrationAlertOperations({
    token,
    alerts,
    integrations,
    setAlerts,
    setIntegrations,
    setWarning,
    ensureDemoWriteAllowed,
    handleWriteApiError,
    retryTask,
  });

  return {
    alerts,
    integrations,
    refreshIntegrations,
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
    retryFailedAlertDeliveries,
  };
}
