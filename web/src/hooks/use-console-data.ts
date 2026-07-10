import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import i18n from "@/i18n";
import { useRefreshInterval } from "@/hooks/use-user-preferences";
import { useVisibilityPolling } from "@/hooks/use-visibility-polling";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import { formatTimeOnly } from "@/lib/api/core";

// mock 数据仅在 demo 模式下动态导入，避免生产包包含 mock 代码
const loadMocks = () => import("@/data/mock");
import { deriveOverview } from "@/hooks/use-console-data.utils";
import { useNodesDomain } from "@/hooks/use-console-nodes-domain";
import { useTasksDomain } from "@/hooks/use-console-tasks-domain";
import { usePoliciesDomain } from "@/hooks/use-console-policies-domain";
import { useAlertsIntegrationsDomain } from "@/hooks/use-console-alerts-integrations-domain";
import type {
  AlertDeliveryRecord,
  AlertBulkRetryResult,
  AlertDeliveryRetryResult,
  AlertDeliveryStats,
  AlertRecord,
  IntegrationChannel,
  IntegrationProbeResult,
  LogEvent,
  NewIntegrationInput,
  NewNodeInput,
  NewPolicyInput,
  NewSSHKeyInput,
  NewTaskInput,
  NodeRecord,
  OverviewStats,
  OverviewSummary,
  HealthIncidentTimelineData,
  OverviewTrafficSeries,
  OverviewTrafficWindow,
  PolicyRecord,
  SSHKeyRecord,
  TaskRecord
} from "@/types/domain";

export interface ConsoleDataState {
  overview: OverviewStats;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  alerts: AlertRecord[];
  integrations: IntegrationChannel[];
  sshKeys: SSHKeyRecord[];
  loading: boolean;
  warning: string | null;
  lastSyncedAt: string;
  refreshVersion: number;
  globalSearch: string;
  setGlobalSearch: (keyword: string) => void;
  refresh: () => void;
  fetchOverviewTraffic: (window: OverviewTrafficWindow, options?: { signal?: AbortSignal }) => Promise<OverviewTrafficSeries>;
  fetchHealthIncidentTimeline: (options?: { windowHours?: number; signal?: AbortSignal }) => Promise<HealthIncidentTimelineData>;
  refreshNodes: (options?: { limit?: number; offset?: number }) => Promise<void>;
  refreshPolicies: () => Promise<void>;
  refreshTasks: (options?: { limit?: number; offset?: number }) => Promise<void>;
  refreshSSHKeys: () => Promise<void>;
  refreshIntegrations: () => Promise<void>;

  createNode: (input: NewNodeInput) => Promise<number>;
  updateNode: (nodeId: number, input: NewNodeInput) => Promise<void>;
  deleteNode: (nodeId: number) => Promise<void>;
  deleteNodes: (nodeIds: number[]) => Promise<{ deleted: number; notFoundIds: number[] }>;
  testNodeConnection: (nodeId: number) => Promise<{ ok: boolean; message: string }>;
  triggerNodeBackup: (nodeId: number) => Promise<void>;

  createPolicy: (input: NewPolicyInput) => Promise<void>;
  updatePolicy: (policyId: number, input: NewPolicyInput) => Promise<void>;
  deletePolicy: (policyId: number) => Promise<void>;
  createTask: (input: NewTaskInput) => Promise<number>;
  updateTask: (taskId: number, input: NewTaskInput) => Promise<void>;
  deleteTask: (taskId: number) => Promise<void>;
  triggerTask: (taskId: number) => Promise<void>;
  cancelTask: (taskId: number) => Promise<void>;
  retryTask: (taskId: number) => Promise<void>;
  pauseTask: (taskId: number, cancelRunning?: boolean) => Promise<void>;
  resumeTask: (taskId: number) => Promise<void>;
  skipNextTask: (taskId: number) => Promise<void>;
  refreshTask: (taskId: number) => Promise<void>;
  fetchTaskLogs: (taskId: number, options?: { beforeId?: number; limit?: number }) => Promise<LogEvent[]>;
  togglePolicy: (policyId: number) => Promise<void>;
  updatePolicySchedule: (policyId: number, cron: string, naturalLanguage: string) => Promise<void>;

  addIntegration: (input: NewIntegrationInput) => Promise<void>;
  removeIntegration: (integrationId: string) => Promise<void>;
  toggleIntegration: (integrationId: string) => Promise<void>;
  updateIntegration: (integrationId: string, patch: Partial<IntegrationChannel> & { secret?: string; skipEndpointHint?: boolean }) => Promise<void>;
  patchIntegration: (integrationId: string, patch: Record<string, unknown>) => Promise<void>;

  createSSHKey: (input: NewSSHKeyInput) => Promise<string>;
  updateSSHKey: (keyId: string, input: NewSSHKeyInput) => Promise<void>;
  deleteSSHKey: (keyId: string) => Promise<boolean>;

  retryAlert: (alertId: string) => Promise<void>;
  acknowledgeAlert: (alertId: string) => Promise<void>;
  resolveAlert: (alertId: string) => Promise<void>;
  fetchAlertDeliveries: (alertId: string) => Promise<AlertDeliveryRecord[]>;
  fetchAlertDeliveryStats: (hours?: number) => Promise<AlertDeliveryStats>;
  retryAlertDelivery: (alertId: string, integrationId: string) => Promise<AlertDeliveryRetryResult>;
  retryFailedAlertDeliveries: (alertId: string) => Promise<AlertBulkRetryResult>;
  testIntegration: (integrationId: string) => Promise<IntegrationProbeResult>;
}

export function useConsoleData(token: string | null): ConsoleDataState {
  const demoModeEnabled = import.meta.env.VITE_ENABLE_DEMO_MODE === "true";

  // 域数组与全局 UI 状态仍由协调者集中持有，避免引入注册式 setter 通道
  // （loadData 需直接写入各域 state，且需与 7 个 context 切片配合做重渲染隔离）。
  const [nodes, setNodes] = useState<NodeRecord[]>([]);
  const [policies, setPolicies] = useState<PolicyRecord[]>([]);
  const [tasks, setTasks] = useState<TaskRecord[]>([]);
  const [alerts, setAlerts] = useState<AlertRecord[]>([]);
  const [integrations, setIntegrations] = useState<IntegrationChannel[]>([]);
  const [sshKeys, setSSHKeys] = useState<SSHKeyRecord[]>([]);
  const [overviewSummary, setOverviewSummary] = useState<OverviewSummary | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [warning, setWarning] = useState<string | null>(null);
  const [globalSearch, setGlobalSearch] = useState("");
  const [lastSyncedAt, setLastSyncedAt] = useState(() => formatTimeOnly(new Date().toISOString()));
  const [refreshVersion, setRefreshVersion] = useState(0);
  const loadAbortRef = useRef<AbortController | null>(null);
  const inventoryVersionRef = useRef(0);
  const taskVersionRef = useRef(0);

  const markInventoryMutated = useCallback(() => {
    inventoryVersionRef.current += 1;
  }, []);

  const markTasksMutated = useCallback(() => {
    taskVersionRef.current += 1;
  }, []);

  const ensureDemoWriteAllowed = useCallback(
    (action: string) => {
      if (demoModeEnabled) {
        return;
      }
      const message = i18n.t("console.actionFailedNoBackend", { action });
      setWarning(message);
      throw new Error(message);
    },
    [demoModeEnabled]
  );

  const handleWriteApiError = useCallback(
    (action: string, error: unknown) => {
      const detail = getErrorMessage(error, i18n.t("console.actionRequestFailed", { action }));
      if (demoModeEnabled) {
        setWarning(detail);
        return;
      }
      const message = i18n.t("console.actionFailed", { action, detail });
      setWarning(message);
      throw error instanceof Error ? error : new Error(message);
    },
    [demoModeEnabled]
  );

  // 逐域接线下沉到独立 domain hook：每个 hook 自持 refreshX 与 abort/版本戳逻辑，
  // 并将协调者的 state/setter/helper 透传给既有操作 hook（操作 hook 签名不变）。
  const nodesDomain = useNodesDomain({
    token,
    demoModeEnabled,
    nodes,
    setNodes,
    policies,
    tasks,
    setTasks,
    setAlerts,
    setSSHKeys,
    setWarning,
    inventoryVersionRef,
    markInventoryMutated,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError,
  });

  const tasksDomain = useTasksDomain({
    token,
    demoModeEnabled,
    nodes,
    policies,
    tasks,
    alerts,
    setTasks,
    setAlerts,
    setWarning,
    taskVersionRef,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError,
  });

  const policiesDomain = usePoliciesDomain({
    token,
    policies,
    setPolicies,
    setTasks,
    setAlerts,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError,
  });

  const alertsIntegrationsDomain = useAlertsIntegrationsDomain({
    token,
    alerts,
    integrations,
    setAlerts,
    setIntegrations,
    setWarning,
    ensureDemoWriteAllowed,
    handleWriteApiError,
    retryTask: tasksDomain.retryTask,
  });

  const loadData = useCallback(async () => {
    loadAbortRef.current?.abort();
    const controller = new AbortController();
    loadAbortRef.current = controller;

    if (!token) {
      if (demoModeEnabled) {
        const mocks = await loadMocks();
        setNodes(mocks.mockNodes);
        setPolicies(mocks.mockPolicies);
        setTasks(mocks.mockTasks);
        setAlerts(mocks.mockAlerts);
        setIntegrations(mocks.mockIntegrations);
        setSSHKeys(mocks.mockSSHKeys);
        setOverviewSummary(mocks.mockOverviewSummary);
        setWarning(null);
        setLoading(false);
        setLastSyncedAt(formatTimeOnly(new Date().toISOString()));
        return;
      }
      setOverviewSummary(null);
      setAlerts([]);
      setWarning(i18n.t("console.notLoggedIn"));
      setLoading(false);
      setLastSyncedAt(formatTimeOnly(new Date().toISOString()));
      return;
    }

    setLoading(true);
    setWarning(null);

    const [alertsResult, overviewResult] = await Promise.allSettled([
      apiClient.getAlerts(token, { signal: controller.signal }),
      apiClient.getOverviewSummary(token, { signal: controller.signal })
    ]);

    if (controller.signal.aborted) {
      return;
    }

    const failedInterfaces: string[] = [];

    if (alertsResult.status === "fulfilled") {
      setAlerts(alertsResult.value);
    } else {
      failedInterfaces.push(i18n.t("console.failedAlerts"));
    }

    if (overviewResult.status === "fulfilled") {
      setOverviewSummary(overviewResult.value);
    } else {
      failedInterfaces.push(i18n.t("console.failedOverview"));
    }

    if (failedInterfaces.length > 0) {
      if (failedInterfaces.length === 2) {
        setWarning(i18n.t("console.allDataLoadFailed"));
      } else {
        setWarning(
          i18n.t("console.partialDataLoadFailed", { interfaces: failedInterfaces.join(i18n.t("console.separator")) })
        );
      }
    }

    setLoading(false);
    setLastSyncedAt(formatTimeOnly(new Date().toISOString()));
  }, [token, demoModeEnabled]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void loadData();
    return () => {
      loadAbortRef.current?.abort();
    };
  }, [loadData]);

  const [refreshIntervalSeconds] = useRefreshInterval();

  // 自动刷新：间隔取自响应式偏好（改设置即时重建定时器，无需重新挂载）；
  // 后台标签页不轮询，切回前台立即补拉一次；关闭刷新（<=0）时停止。
  useVisibilityPolling(
    () => { void loadData(); },
    refreshIntervalSeconds > 0 ? refreshIntervalSeconds * 1000 : 0,
    { enabled: Boolean(token), immediate: false }
  );

  const overview = useMemo(() => deriveOverview(nodes, policies, tasks, overviewSummary), [nodes, overviewSummary, policies, tasks]);

  const fetchOverviewTraffic = useCallback(async (window: OverviewTrafficWindow, options?: { signal?: AbortSignal }): Promise<OverviewTrafficSeries> => {
    if (!token) {
      if (demoModeEnabled) {
        const mocks = await loadMocks();
        return mocks.buildMockOverviewTrafficSeries(window);
      }
      return {
        window,
        bucketMinutes: window === "1h" ? 5 : window === "24h" ? 30 : 180,
        hasRealSamples: false,
        generatedAt: new Date().toISOString(),
        points: []
      };
    }
    return apiClient.getOverviewTraffic(token, { window, signal: options?.signal });
  }, [demoModeEnabled, token]);

  const fetchHealthIncidentTimeline = useCallback(async (options?: { windowHours?: number; signal?: AbortSignal }): Promise<HealthIncidentTimelineData> => {
    if (!token) {
      if (demoModeEnabled) {
        const mocks = await loadMocks();
        return mocks.buildMockHealthIncidentTimeline();
      }
      return {
        generatedAt: new Date().toISOString(),
        windowHours: options?.windowHours ?? 72,
        summary: { total: 0, critical: 0, warning: 0, info: 0 },
        groups: []
      };
    }
    return apiClient.getHealthIncidentTimeline(token, { windowHours: options?.windowHours, signal: options?.signal });
  }, [demoModeEnabled, token]);

  // 稳定 refresh：避免在每次渲染时新建函数，从而破坏
  // app-shell.tsx 中 sharedContextValue 的 useMemo（其依赖项含 refresh）。
  const refresh = useCallback(() => {
    setRefreshVersion((current) => current + 1);
    void loadData();
  }, [loadData]);

  return {
    overview,
    nodes: nodesDomain.nodes,
    policies: policiesDomain.policies,
    tasks: tasksDomain.tasks,
    alerts: alertsIntegrationsDomain.alerts,
    integrations: alertsIntegrationsDomain.integrations,
    sshKeys,
    loading,
    warning,
    lastSyncedAt,
    refreshVersion,
    globalSearch,
    setGlobalSearch,
    fetchOverviewTraffic,
    fetchHealthIncidentTimeline,
    refresh,
    refreshNodes: nodesDomain.refreshNodes,
    refreshPolicies: policiesDomain.refreshPolicies,
    refreshTasks: tasksDomain.refreshTasks,
    refreshSSHKeys: nodesDomain.refreshSSHKeys,
    refreshIntegrations: alertsIntegrationsDomain.refreshIntegrations,

    createNode: nodesDomain.createNode,
    updateNode: nodesDomain.updateNode,
    deleteNode: nodesDomain.deleteNode,
    deleteNodes: nodesDomain.deleteNodes,
    testNodeConnection: nodesDomain.testNodeConnection,
    triggerNodeBackup: nodesDomain.triggerNodeBackup,

    createPolicy: policiesDomain.createPolicy,
    updatePolicy: policiesDomain.updatePolicy,
    deletePolicy: policiesDomain.deletePolicy,
    createTask: tasksDomain.createTask,
    updateTask: tasksDomain.updateTask,
    deleteTask: tasksDomain.deleteTask,
    triggerTask: tasksDomain.triggerTask,
    cancelTask: tasksDomain.cancelTask,
    retryTask: tasksDomain.retryTask,
    pauseTask: tasksDomain.pauseTask,
    resumeTask: tasksDomain.resumeTask,
    skipNextTask: tasksDomain.skipNextTask,
    refreshTask: tasksDomain.refreshTask,
    fetchTaskLogs: tasksDomain.fetchTaskLogs,
    togglePolicy: policiesDomain.togglePolicy,
    updatePolicySchedule: policiesDomain.updatePolicySchedule,

    addIntegration: alertsIntegrationsDomain.addIntegration,
    removeIntegration: alertsIntegrationsDomain.removeIntegration,
    toggleIntegration: alertsIntegrationsDomain.toggleIntegration,
    updateIntegration: alertsIntegrationsDomain.updateIntegration,
    patchIntegration: alertsIntegrationsDomain.patchIntegration,

    createSSHKey: nodesDomain.createSSHKey,
    updateSSHKey: nodesDomain.updateSSHKey,
    deleteSSHKey: nodesDomain.deleteSSHKey,

    retryAlert: alertsIntegrationsDomain.retryAlert,
    acknowledgeAlert: alertsIntegrationsDomain.acknowledgeAlert,
    resolveAlert: alertsIntegrationsDomain.resolveAlert,
    fetchAlertDeliveries: alertsIntegrationsDomain.fetchAlertDeliveries,
    fetchAlertDeliveryStats: alertsIntegrationsDomain.fetchAlertDeliveryStats,
    retryAlertDelivery: alertsIntegrationsDomain.retryAlertDelivery,
    retryFailedAlertDeliveries: alertsIntegrationsDomain.retryFailedAlertDeliveries,
    testIntegration: alertsIntegrationsDomain.testIntegration
  };
}
