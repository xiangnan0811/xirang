import {
  useCallback,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type Dispatch,
  type SetStateAction,
} from "react";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import { apiClient } from "@/lib/api/client";
import { getAuthIdentitySnapshot, isAuthTransitionActive, subscribeAuthTransition } from "@/lib/api/core";
import { getNotificationContextEpoch, subscribeNotificationContextEpoch } from "@/lib/notification-config-context";
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
  retryTask: (taskId: number, isCurrent?: () => boolean) => Promise<void>;
};

const NO_INTEGRATIONS: IntegrationChannel[] = [];

// 告警 + 集成域：自持 refreshIntegrations 与 abort 控制，
// 接线 useIntegrationAlertOperations（与 retryTask 强耦合，故合并为一域）。
// 共享渠道集合位于 Shell 上方，必须按 epoch/token/role 标记所属身份：
// 身份或屏障变化在当次渲染即屏蔽并清空，旧读取/写回执无法写入新身份或 demo 集合。
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
  const { role } = useAuth();
  const epoch = useSyncExternalStore(subscribeNotificationContextEpoch, getNotificationContextEpoch, getNotificationContextEpoch);
  const transitioning = useSyncExternalStore(subscribeAuthTransition, isAuthTransitionActive, isAuthTransitionActive);

  const [collectionOwner, setCollectionOwner] = useState<{ epoch: string; token: string | null; role: AuthRole | null }>(
    () => ({ epoch, token, role }),
  );
  const ownsCollection = collectionOwner.epoch === epoch && collectionOwner.token === token && collectionOwner.role === role;
  if (!ownsCollection) {
    // 渲染期同步改标并归零；本次渲染输出已屏蔽，不等待 effect 清理。
    setCollectionOwner({ epoch, token, role });
    setIntegrations([]);
  }
  const visibleIntegrations = ownsCollection && !transitioning ? integrations : NO_INTEGRATIONS;

  const mountedRef = useRef(false);
  const mountRevisionRef = useRef(0);
  const refreshIntegrationsAbortRef = useRef<AbortController | null>(null);
  const refreshAttemptRef = useRef(0);

  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      mountRevisionRef.current += 1;
    };
  }, []);

  useLayoutEffect(() => () => {
    refreshAttemptRef.current += 1;
    refreshIntegrationsAbortRef.current?.abort();
    refreshIntegrationsAbortRef.current = null;
  }, [epoch, token, role]);

  // 绑定本次渲染的身份；A→B→A、同 token 重新登录或屏障均推进 epoch 使旧闭包失效。
  const isCurrentOwner = useCallback(() => {
    const current = getAuthIdentitySnapshot();
    return mountedRef.current && epoch === getNotificationContextEpoch() && !isAuthTransitionActive()
      && current.token === token && current.role === role;
  }, [epoch, token, role]);

  const isCurrentIntegrationOwner = useCallback(
    () => isCurrentOwner() && (token === null || role === "admin"),
    [isCurrentOwner, role, token],
  );

  // 写入前及 React 实际执行 functional updater 时都核对所属身份。
  const setOwnedIntegrations = useCallback<Dispatch<SetStateAction<IntegrationChannel[]>>>((action) => {
    if (!isCurrentOwner()) return;
    setIntegrations((prev) => {
      if (!isCurrentOwner()) return prev;
      return typeof action === "function" ? action(prev) : action;
    });
  }, [isCurrentOwner, setIntegrations]);

  const refreshIntegrations = useCallback(async () => {
    if (!token) return;
    refreshIntegrationsAbortRef.current?.abort();
    refreshIntegrationsAbortRef.current = null;
    const attempt = ++refreshAttemptRef.current;
    const mountRevision = mountRevisionRef.current;
    if (!isCurrentOwner()) return;
    const controller = new AbortController();
    refreshIntegrationsAbortRef.current = controller;
    try {
      const result = await apiClient.getIntegrations(token, { signal: controller.signal });
      if (controller.signal.aborted || attempt !== refreshAttemptRef.current
        || mountRevision !== mountRevisionRef.current || !isCurrentOwner()) return;
      setOwnedIntegrations(result);
    } catch {
      // 按需刷新失败时静默处理
    } finally {
      if (refreshIntegrationsAbortRef.current === controller) {
        refreshIntegrationsAbortRef.current = null;
      }
    }
  }, [token, isCurrentOwner, setOwnedIntegrations]);

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
    integrations: visibleIntegrations,
    setAlerts,
    setIntegrations: setOwnedIntegrations,
    setWarning,
    ensureDemoWriteAllowed,
    handleWriteApiError,
    retryTask,
    isCurrentIntegrationOwner,
  });

  return {
    alerts,
    integrations: visibleIntegrations,
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
