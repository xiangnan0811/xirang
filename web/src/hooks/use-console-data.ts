import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
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
  PolicyRecord,
  SSHKeyRecord,
  TaskRecord,
  TrafficPoint
} from "@/types/domain";

export interface ConsoleDataState {
  overview: OverviewStats;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  trafficSeries: TrafficPoint[];
  alerts: AlertRecord[];
  integrations: IntegrationChannel[];
  sshKeys: SSHKeyRecord[];
  loading: boolean;
  warning: string | null;
  lastSyncedAt: string;
  globalSearch: string;
  setGlobalSearch: (keyword: string) => void;
  refresh: () => void;

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
  updateIntegration: (integrationId: string, patch: Partial<IntegrationChannel>) => Promise<void>;

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
  const [loading, setLoading] = useState<boolean>(true);
  const [warning, setWarning] = useState<string | null>(null);
  const [globalSearch, setGlobalSearch] = useState("");
  const [lastSyncedAt, setLastSyncedAt] = useState(() => new Date().toLocaleTimeString("zh-CN"));
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
      const message = `${action}失败：当前未连接后端，请检查登录态或服务状态后重试。`;
      setWarning(message);
      throw new Error(message);
    },
    [demoModeEnabled]
  );

  const handleWriteApiError = useCallback(
    (action: string, error: unknown) => {
      const detail = getErrorMessage(error, `${action}请求失败`);
      if (demoModeEnabled) {
        setWarning(detail);
        return;
      }
      const message = `${action}失败：${detail}`;
      setWarning(message);
      throw error instanceof Error ? error : new Error(message);
    },
    [demoModeEnabled]
  );

  const loadData = useCallback(async () => {
    loadAbortRef.current?.abort();
    const controller = new AbortController();
    loadAbortRef.current = controller;
    const inventoryVersionAtStart = inventoryVersionRef.current;
    const taskVersionAtStart = taskVersionRef.current;

    if (!token) {
      setNodes([]);
      setPolicies([]);
      setTasks([]);
      setAlerts([]);
      setIntegrations([]);
      setSSHKeys([]);
      setWarning("未检测到登录态，请重新登录后刷新数据。");
      setLoading(false);
      setLastSyncedAt(new Date().toLocaleTimeString("zh-CN"));
      return;
    }

    setLoading(true);
    setWarning(null);

    const [
      nodesResult,
      policiesResult,
      tasksResult,
      alertsResult,
      sshKeysResult,
      integrationsResult
    ] = await Promise.allSettled([
      apiClient.getNodes(token, { signal: controller.signal }),
      apiClient.getPolicies(token, { signal: controller.signal }),
      apiClient.getTasks(token, { signal: controller.signal }),
      apiClient.getAlerts(token, { signal: controller.signal }),
      apiClient.getSSHKeys(token, { signal: controller.signal }),
      apiClient.getIntegrations(token, { signal: controller.signal })
    ]);

    if (controller.signal.aborted) {
      return;
    }

    const failedInterfaces: string[] = [];

    if (nodesResult.status === "fulfilled" && inventoryVersionAtStart === inventoryVersionRef.current) {
      setNodes(nodesResult.value);
    } else if (nodesResult.status !== "fulfilled") {
      failedInterfaces.push("节点");
    }

    if (policiesResult.status === "fulfilled") {
      setPolicies(policiesResult.value);
    } else {
      failedInterfaces.push("策略");
    }

    if (tasksResult.status === "fulfilled" && taskVersionAtStart === taskVersionRef.current) {
      setTasks(tasksResult.value);
    } else if (tasksResult.status !== "fulfilled") {
      failedInterfaces.push("任务");
    }

    if (alertsResult.status === "fulfilled") {
      setAlerts(alertsResult.value);
    } else {
      failedInterfaces.push("告警");
    }

    if (sshKeysResult.status === "fulfilled" && inventoryVersionAtStart === inventoryVersionRef.current) {
      setSSHKeys(sshKeysResult.value);
    } else if (sshKeysResult.status !== "fulfilled") {
      failedInterfaces.push("SSH Key");
    }

    if (integrationsResult.status === "fulfilled") {
      setIntegrations(integrationsResult.value);
    } else {
      failedInterfaces.push("通知通道");
    }

    if (failedInterfaces.length > 0) {
      if (failedInterfaces.length === 6) {
        setWarning(
          "登录后数据加载失败：当前无法从后端获取任何控制台数据。\n请先点击顶部“刷新数据”重试；若仍失败，请检查后端服务状态、网络连通性与 VITE_API_BASE_URL 配置，并重新登录。"
        );
      } else {
        setWarning(
          `部分数据加载失败（${failedInterfaces.join("、")}）。已保留已成功加载的数据。\n请点击顶部“刷新数据”重试；若持续失败，请检查后端服务状态或重新登录。`
        );
      }
    }

    setLoading(false);
    setLastSyncedAt(new Date().toLocaleTimeString("zh-CN"));
  }, [token]);

  useEffect(() => {
    void loadData();
    return () => {
      loadAbortRef.current?.abort();
    };
  }, [loadData]);

  const overview = useMemo(() => deriveOverview(nodes, policies, tasks), [nodes, policies, tasks]);

  const trafficSeries = useMemo<TrafficPoint[]>(() => {
    // 注意: 当前流量数据为基于运行中任务速度的估算值，非真实网络流量
    const now = new Date();
    const labels = Array.from({ length: 12 }, (_, index) => {
      const pointTime = new Date(now.getTime() - (11 - index) * 5 * 60 * 1000);
      return `${pointTime.getHours().toString().padStart(2, "0")}:${pointTime.getMinutes().toString().padStart(2, "0")}`;
    });

    const running = tasks.filter((task) => task.status === "running" || task.status === "retrying");
    const avgSpeed = running.length > 0
      ? Math.round(running.reduce((sum, task) => sum + (task.speedMbps || 0), 0) / running.length)
      : 0;

    return labels.map((label) => ({
      label,
      ingressMbps: avgSpeed,
      egressMbps: Math.max(0, Math.round(avgSpeed * 0.82))
    }));
  }, [tasks]);

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
    trafficSeries,
    alerts,
    integrations,
    sshKeys,
    loading,
    warning,
    lastSyncedAt,
    globalSearch,
    setGlobalSearch,
    refresh: () => {
      void loadData();
    },

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
