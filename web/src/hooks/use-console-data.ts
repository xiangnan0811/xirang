import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import {
  buildMockOverviewTrafficSeries,
  mockAlerts,
  mockIntegrations,
  mockNodes,
  mockOverviewSummary,
  mockPolicies,
  mockSSHKeys,
  mockTasks,
} from "@/data/mock";
import { getErrorMessage } from "@/lib/utils";
import { useIntegrationAlertOperations } from "@/hooks/use-console-integration-alert-operations";
import { useNodeOperations } from "@/hooks/use-console-node-operations";
import { usePolicyOperations } from "@/hooks/use-console-policy-operations";
import { useTaskOperations } from "@/hooks/use-console-task-operations";
import {
  deriveOverview
} from "@/hooks/use-console-data.utils";
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
  refreshTask: (taskId: number) => Promise<void>;
  fetchTaskLogs: (taskId: number, options?: { beforeId?: number; limit?: number }) => Promise<LogEvent[]>;
  togglePolicy: (policyId: number) => Promise<void>;
  updatePolicySchedule: (policyId: number, cron: string, naturalLanguage: string) => Promise<void>;

  addIntegration: (input: NewIntegrationInput) => Promise<void>;
  removeIntegration: (integrationId: string) => Promise<void>;
  toggleIntegration: (integrationId: string) => Promise<void>;
  updateIntegration: (integrationId: string, patch: Partial<IntegrationChannel> & { secret?: string; skipEndpointHint?: boolean }) => Promise<void>;

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
  const [lastSyncedAt, setLastSyncedAt] = useState(() => new Date().toLocaleTimeString("zh-CN"));
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

  const loadData = useCallback(async () => {
    loadAbortRef.current?.abort();
    const controller = new AbortController();
    loadAbortRef.current = controller;

    if (!token) {
      if (demoModeEnabled) {
        setNodes(mockNodes);
        setPolicies(mockPolicies);
        setTasks(mockTasks);
        setAlerts(mockAlerts);
        setIntegrations(mockIntegrations);
        setSSHKeys(mockSSHKeys);
        setOverviewSummary(mockOverviewSummary);
        setWarning(null);
        setLoading(false);
        setLastSyncedAt(new Date().toLocaleTimeString("zh-CN"));
        return;
      }
      setOverviewSummary(null);
      setAlerts([]);
      setWarning(i18n.t("console.notLoggedIn"));
      setLoading(false);
      setLastSyncedAt(new Date().toLocaleTimeString("zh-CN"));
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
    setLastSyncedAt(new Date().toLocaleTimeString("zh-CN"));
  }, [token, demoModeEnabled]);

  const refreshNodes = useCallback(async (_options?: { limit?: number; offset?: number }) => {
    if (!token) return;
    const controller = new AbortController();
    const inventoryVersionAtStart = inventoryVersionRef.current;
    try {
      const result = await apiClient.getNodes(token, { signal: controller.signal });
      if (inventoryVersionAtStart === inventoryVersionRef.current) {
        setNodes(result);
      }
    } catch {
      // 按需刷新失败时静默处理，不覆盖全局 warning
    }
  }, [token]);

  const refreshPolicies = useCallback(async () => {
    if (!token) return;
    try {
      const result = await apiClient.getPolicies(token);
      setPolicies(result);
    } catch {
      // 按需刷新失败时静默处理
    }
  }, [token]);

  const refreshTasks = useCallback(async (_options?: { limit?: number; offset?: number }) => {
    if (!token) return;
    const taskVersionAtStart = taskVersionRef.current;
    try {
      const result = await apiClient.getTasks(token);
      if (taskVersionAtStart === taskVersionRef.current) {
        setTasks(result);
      }
    } catch {
      // 按需刷新失败时静默处理
    }
  }, [token]);

  const refreshSSHKeys = useCallback(async () => {
    if (!token) return;
    const inventoryVersionAtStart = inventoryVersionRef.current;
    try {
      const result = await apiClient.getSSHKeys(token);
      if (inventoryVersionAtStart === inventoryVersionRef.current) {
        setSSHKeys(result);
      }
    } catch {
      // 按需刷新失败时静默处理
    }
  }, [token]);

  const refreshIntegrations = useCallback(async () => {
    if (!token) return;
    try {
      const result = await apiClient.getIntegrations(token);
      setIntegrations(result);
    } catch {
      // 按需刷新失败时静默处理
    }
  }, [token]);

  useEffect(() => {
    void loadData();
    return () => {
      loadAbortRef.current?.abort();
    };
  }, [loadData]);

  useEffect(() => {
    if (!token) return;
    const interval = setInterval(() => {
      void loadData();
    }, 60_000); // 每 60 秒自动刷新
    return () => clearInterval(interval);
  }, [token, loadData]);

  const overview = useMemo(() => deriveOverview(nodes, policies, tasks, overviewSummary), [nodes, overviewSummary, policies, tasks]);

  const fetchOverviewTraffic = useCallback(async (window: OverviewTrafficWindow, options?: { signal?: AbortSignal }): Promise<OverviewTrafficSeries> => {
    if (!token) {
      if (demoModeEnabled) {
        return buildMockOverviewTrafficSeries(window);
      }
      return {
        window,
        bucketMinutes: window === "1h" ? 5 : window === "24h" ? 60 : 360,
        hasRealSamples: false,
        generatedAt: new Date().toISOString(),
        points: []
      };
    }
    return apiClient.getOverviewTraffic(token, { window, signal: options?.signal });
  }, [demoModeEnabled, token]);

  const {
    createSSHKey,
    updateSSHKey,
    deleteSSHKey,
    createNode,
    updateNode,
    deleteNode,
    deleteNodes,
    testNodeConnection,
    triggerNodeBackup
  } = useNodeOperations({
    token,
    demoModeEnabled,
    nodes,
    policies,
    tasks,
    setNodes,
    setTasks,
    setAlerts,
    setSSHKeys,
    setWarning,
    markInventoryMutated,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError
  });

  const {
    createTask,
    updateTask,
    deleteTask,
    triggerTask,
    cancelTask,
    retryTask,
    refreshTask,
    fetchTaskLogs
  } = useTaskOperations({
    token,
    nodes,
    policies,
    tasks,
    alerts,
    setTasks,
    setAlerts,
    setWarning,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError
  });

  const {
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy,
    updatePolicySchedule
  } = usePolicyOperations({
    token,
    policies,
    setPolicies,
    setTasks,
    setAlerts,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError
  });

  const {
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
  } = useIntegrationAlertOperations({
    token,
    alerts,
    integrations,
    setAlerts,
    setIntegrations,
    setWarning,
    ensureDemoWriteAllowed,
    handleWriteApiError,
    retryTask
  });

  return {
    overview,
    nodes,
    policies,
    tasks,
    alerts,
    integrations,
    sshKeys,
    loading,
    warning,
    lastSyncedAt,
    refreshVersion,
    globalSearch,
    setGlobalSearch,
    fetchOverviewTraffic,
    refresh: () => {
      setRefreshVersion((current) => current + 1);
      void loadData();
    },
    refreshNodes,
    refreshPolicies,
    refreshTasks,
    refreshSSHKeys,
    refreshIntegrations,

    createNode,
    updateNode,
    deleteNode,
    deleteNodes,
    testNodeConnection,
    triggerNodeBackup,

    createPolicy,
    updatePolicy,
    deletePolicy,
    createTask,
    updateTask,
    deleteTask,
    triggerTask,
    cancelTask,
    retryTask,
    refreshTask,
    fetchTaskLogs,
    togglePolicy,
    updatePolicySchedule,

    addIntegration,
    removeIntegration,
    toggleIntegration,
    updateIntegration,

    createSSHKey,
    updateSSHKey,
    deleteSSHKey,

    retryAlert,
    acknowledgeAlert,
    resolveAlert,
    fetchAlertDeliveries,
    fetchAlertDeliveryStats,
    retryAlertDelivery,
    retryFailedAlertDeliveries,
    testIntegration
  };
}
