import { useCallback, useEffect, useMemo, useState } from "react";
import { ApiError, apiClient } from "@/lib/api/client";
import {
  buildFingerprint,
  createIntegrationId,
  createKeyId,
  deriveOverview,
  describeCron,
  parseTags
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
  NodeExecResult,
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
  execNodeCommand: (nodeId: number, command: string, timeoutSeconds?: number) => Promise<NodeExecResult>;

  createPolicy: (input: NewPolicyInput) => Promise<void>;
  updatePolicy: (policyId: number, input: NewPolicyInput) => Promise<void>;
  deletePolicy: (policyId: number) => Promise<void>;
  createTask: (input: NewTaskInput) => Promise<number>;
  deleteTask: (taskId: number) => Promise<void>;
  triggerTask: (taskId: number) => Promise<void>;
  cancelTask: (taskId: number) => Promise<void>;
  retryTask: (taskId: number) => Promise<void>;
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
      const detail = error instanceof Error ? error.message : `${action}请求失败`;
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
      apiClient.getNodes(token),
      apiClient.getPolicies(token),
      apiClient.getTasks(token),
      apiClient.getAlerts(token),
      apiClient.getSSHKeys(token),
      apiClient.getIntegrations(token)
    ]);

    const failedInterfaces: string[] = [];

    if (nodesResult.status === "fulfilled") {
      setNodes(nodesResult.value);
    } else {
      failedInterfaces.push("节点");
    }

    if (policiesResult.status === "fulfilled") {
      setPolicies(policiesResult.value);
    } else {
      failedInterfaces.push("策略");
    }

    if (tasksResult.status === "fulfilled") {
      setTasks(tasksResult.value);
    } else {
      failedInterfaces.push("任务");
    }

    if (alertsResult.status === "fulfilled") {
      setAlerts(alertsResult.value);
    } else {
      failedInterfaces.push("告警");
    }

    if (sshKeysResult.status === "fulfilled") {
      setSSHKeys(sshKeysResult.value);
    } else {
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

  const createSSHKey = useCallback(async (input: NewSSHKeyInput): Promise<string> => {
    if (token) {
      try {
        const created = await apiClient.createSSHKey(token, input);
        setSSHKeys((prev) => [created, ...prev]);
        return created.id;
      } catch (error) {
        handleWriteApiError("创建 SSH Key", error);
        return "";
      }
    } else {
      ensureDemoWriteAllowed("创建 SSH Key");
    }

    const nextId = createKeyId(input.name || input.username || "ssh-key");
    const item: SSHKeyRecord = {
      id: nextId,
      name: input.name,
      username: input.username,
      keyType: input.keyType,
      privateKey: input.privateKey,
      fingerprint: buildFingerprint(input.privateKey),
      createdAt: new Date().toLocaleString("zh-CN", { hour12: false })
    };
    setSSHKeys((prev) => [item, ...prev]);
    return nextId;
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const updateSSHKey = useCallback(async (keyId: string, input: NewSSHKeyInput) => {
    if (token) {
      try {
        const updated = await apiClient.updateSSHKey(token, keyId, input);
        setSSHKeys((prev) => prev.map((item) => (item.id === keyId ? updated : item)));
        return;
      } catch (error) {
        handleWriteApiError("更新 SSH Key", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("更新 SSH Key");
    }

    setSSHKeys((prev) =>
      prev.map((item) =>
        item.id === keyId
          ? {
              ...item,
              name: input.name,
              username: input.username,
              keyType: input.keyType,
              privateKey: input.privateKey,
              fingerprint: buildFingerprint(input.privateKey)
            }
          : item
      )
    );
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const deleteSSHKey = useCallback(async (keyId: string): Promise<boolean> => {
    const usedByNodes = nodes.some((node) => node.keyId === keyId);
    if (usedByNodes) {
      return false;
    }

    if (token) {
      try {
        await apiClient.deleteSSHKey(token, keyId);
      } catch (error) {
        if (error instanceof ApiError) {
          if (demoModeEnabled) {
            setWarning(typeof error.detail === "string" ? error.detail : "删除 SSH Key 失败");
          } else {
            const detail = typeof error.detail === "string" ? error.detail : error.message;
            const message = `删除 SSH Key 失败：${detail}`;
            setWarning(message);
            throw new Error(message);
          }
        } else if (!demoModeEnabled) {
          handleWriteApiError("删除 SSH Key", error);
        }
        if (!demoModeEnabled) {
          return false;
        }
      }
    } else {
      ensureDemoWriteAllowed("删除 SSH Key");
    }

    setSSHKeys((prev) => prev.filter((item) => item.id !== keyId));
    return true;
  }, [demoModeEnabled, ensureDemoWriteAllowed, handleWriteApiError, nodes, token]);

  const createNode = useCallback(async (input: NewNodeInput): Promise<number> => {
    let keyId = input.keyId ?? null;

    if (input.authType === "key" && input.inlinePrivateKey?.trim()) {
      keyId = await createSSHKey({
        name: input.inlineKeyName?.trim() || `${input.name}-key`,
        username: input.username,
        keyType: input.inlineKeyType ?? "auto",
        privateKey: input.inlinePrivateKey.trim()
      });
    }

    const finalInput: NewNodeInput = {
      ...input,
      keyId
    };

    if (token) {
      try {
        const created = await apiClient.createNode(token, finalInput);
        setNodes((prev) => [created, ...prev]);
        return created.id;
      } catch (error) {
        handleWriteApiError("创建节点", error);
        return -1;
      }
    } else {
      ensureDemoWriteAllowed("创建节点");
    }

    const maxNodeID = nodes.length > 0 ? Math.max(...nodes.map((node) => node.id)) : 0;
    const nextNode: NodeRecord = {
      id: maxNodeID + 1,
      name: input.name,
      host: input.host,
      address: input.host,
      ip: input.host,
      port: input.port || 22,
      username: input.username,
      authType: input.authType,
      keyId,
      basePath: input.basePath || "/",
      status: "warning",
      tags: parseTags(input.tags),
      lastSeenAt: "尚未探测",
      lastBackupAt: "尚未执行",
      diskFreePercent: 100,
      diskUsedGb: 0,
      diskTotalGb: 800,
      successRate: 100
    };
    setNodes((prev) => [nextNode, ...prev]);
    return nextNode.id;
  }, [createSSHKey, ensureDemoWriteAllowed, handleWriteApiError, nodes, token]);

  const updateNode = useCallback(async (nodeID: number, input: NewNodeInput) => {
    let keyId = input.keyId ?? null;
    if (input.authType === "key" && input.inlinePrivateKey?.trim()) {
      keyId = await createSSHKey({
        name: input.inlineKeyName?.trim() || `${input.name}-key`,
        username: input.username,
        keyType: input.inlineKeyType ?? "auto",
        privateKey: input.inlinePrivateKey.trim()
      });
    }

    const finalInput: NewNodeInput = {
      ...input,
      keyId
    };

    if (token) {
      try {
        const updated = await apiClient.updateNode(token, nodeID, finalInput);
        setNodes((prev) => prev.map((node) => (node.id === nodeID ? updated : node)));
        return;
      } catch (error) {
        handleWriteApiError("更新节点", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("更新节点");
    }

    setNodes((prev) =>
      prev.map((node) =>
        node.id === nodeID
          ? {
              ...node,
              name: input.name,
              host: input.host,
              address: input.host,
              ip: input.host,
              port: input.port || 22,
              username: input.username,
              authType: input.authType,
              keyId,
              basePath: input.basePath || "/",
              tags: parseTags(input.tags)
            }
          : node
      )
    );
  }, [createSSHKey, ensureDemoWriteAllowed, handleWriteApiError, token]);

  const deleteNode = useCallback(async (nodeID: number) => {
    if (token) {
      try {
        await apiClient.deleteNode(token, nodeID);
      } catch (error) {
        handleWriteApiError("删除节点", error);
      }
    } else {
      ensureDemoWriteAllowed("删除节点");
    }

    const nodeName = nodes.find((node) => node.id === nodeID)?.name;
    setNodes((prev) => prev.filter((node) => node.id !== nodeID));
    setTasks((prev) => prev.filter((task) => task.nodeId !== nodeID));
    if (nodeName) {
      setAlerts((prev) => prev.filter((alert) => alert.nodeName !== nodeName));
    }
  }, [ensureDemoWriteAllowed, handleWriteApiError, nodes, token]);

  const deleteNodes = useCallback(async (nodeIDs: number[]): Promise<{ deleted: number; notFoundIds: number[] }> => {
    const normalized = Array.from(new Set(nodeIDs.filter((item) => Number.isFinite(item) && item > 0)));
    if (!normalized.length) {
      return {
        deleted: 0,
        notFoundIds: []
      };
    }

    if (token) {
      try {
        const result = await apiClient.deleteNodes(token, normalized);
        const deletedSet = new Set(normalized.filter((id) => !result.notFoundIds.includes(id)));
        setNodes((prev) => prev.filter((node) => !deletedSet.has(node.id)));
        setTasks((prev) => prev.filter((task) => !deletedSet.has(task.nodeId)));
        setAlerts((prev) => prev.filter((alert) => !deletedSet.has(alert.nodeId)));
        return result;
      } catch (error) {
        handleWriteApiError("批量删除节点", error);
        return { deleted: 0, notFoundIds: normalized };
      }
    } else {
      ensureDemoWriteAllowed("批量删除节点");
    }

    const deletedSet = new Set(normalized);
    setNodes((prev) => prev.filter((node) => !deletedSet.has(node.id)));
    setTasks((prev) => prev.filter((task) => !deletedSet.has(task.nodeId)));
    setAlerts((prev) => prev.filter((alert) => !deletedSet.has(alert.nodeId)));

    return {
      deleted: normalized.length,
      notFoundIds: []
    };
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const testNodeConnection = useCallback(async (nodeID: number): Promise<{ ok: boolean; message: string }> => {
    if (token) {
      try {
        const result = await apiClient.testNodeConnection(token, nodeID);
        const probeTime = new Date().toLocaleString("zh-CN", { hour12: false });
        setNodes((prev) =>
          prev.map((node) =>
            node.id === nodeID
              ? {
                  ...node,
                  status: result.ok ? "online" : "offline",
                  lastSeenAt: probeTime,
                  diskProbeAt: probeTime,
                  connectionLatencyMs: result.ok ? result.latency_ms : undefined,
                  diskUsedGb: result.disk_used_gb ?? node.diskUsedGb,
                  diskTotalGb: result.disk_total_gb ?? node.diskTotalGb,
                  diskFreePercent: result.disk_total_gb
                    ? Math.max(
                        1,
                        Math.round(
                          ((result.disk_total_gb - (result.disk_used_gb ?? node.diskUsedGb)) /
                            result.disk_total_gb) *
                            100
                        )
                      )
                    : node.diskFreePercent
                }
              : node
          )
        );
        return {
          ok: result.ok,
          message: result.message
        };
      } catch (error) {
        handleWriteApiError("节点连通性探测", error);
        return { ok: false, message: "探测请求失败" };
      }
    } else {
      ensureDemoWriteAllowed("节点连通性探测");
    }

    await new Promise((resolve) => setTimeout(resolve, 650));

    const now = new Date();
    const seed = (now.getSeconds() + nodeID * 17) % 10;
    const ok = seed >= 2;
    const probeTime = now.toLocaleString("zh-CN", { hour12: false });

    setNodes((prev) =>
      prev.map((node) => {
        if (node.id !== nodeID) {
          return node;
        }
        if (!ok) {
          return {
            ...node,
            status: "offline",
            lastSeenAt: probeTime,
            diskProbeAt: probeTime,
            connectionLatencyMs: undefined
          };
        }

        const total = node.diskTotalGb || 800;
        const used = Math.max(10, Math.min(total - 5, node.diskUsedGb + ((seed % 3) - 1) * 8));

        return {
          ...node,
          status: used / total >= 0.9 ? "warning" : "online",
          lastSeenAt: probeTime,
          diskProbeAt: probeTime,
          connectionLatencyMs: 18 + (seed * 11) % 120,
          diskUsedGb: used,
          diskFreePercent: Math.max(1, Math.round(((total - used) / total) * 100))
        };
      })
    );

    return ok
      ? { ok: true, message: "SSH 握手成功，已更新磁盘探测信息。" }
      : { ok: false, message: "连接失败：SSH 握手超时或认证失败。" };
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const triggerNodeBackup = useCallback(
    async (nodeID: number) => {
      const node = nodes.find((item) => item.id === nodeID);
      if (!node) {
        return;
      }

      const nextTaskID = tasks.length > 0 ? Math.max(...tasks.map((task) => task.id)) + 1 : 5000;
      const targetPolicy = policies.find((policy) => policy.enabled) ?? policies[0];

      if (token) {
        try {
          const created = await apiClient.createTask(token, {
            name: `${node.name} 手动备份`,
            nodeId: nodeID,
            policyId: targetPolicy?.id ?? null,
            executorType: "rsync",
            rsyncSource: targetPolicy?.sourcePath,
            rsyncTarget: targetPolicy?.targetPath,
            cronSpec: targetPolicy?.cron
          });
          setTasks((prev) => [created, ...prev]);
          return;
        } catch (error) {
          handleWriteApiError("触发节点手动备份", error);
          return;
        }
      } else {
        ensureDemoWriteAllowed("触发节点手动备份");
      }

      const nextTask: TaskRecord = {
        id: nextTaskID,
        policyName: targetPolicy?.name ?? "手动备份",
        nodeName: node.name,
        nodeId: nodeID,
        status: "running",
        progress: 6,
        startedAt: new Date().toLocaleString("zh-CN", { hour12: false }),
        speedMbps: 96
      };

      setTasks((prev) => [nextTask, ...prev]);
      setNodes((prev) =>
        prev.map((item) =>
          item.id === nodeID
            ? {
                ...item,
                status: "online",
                lastSeenAt: "刚刚"
              }
            : item
        )
      );
    },
    [ensureDemoWriteAllowed, handleWriteApiError, nodes, policies, tasks, token]
  );

  const execNodeCommand = useCallback(async (nodeID: number, command: string, timeoutSeconds = 20): Promise<NodeExecResult> => {
    const normalizedCommand = command.trim();
    if (!normalizedCommand) {
      throw new Error("命令不能为空");
    }

    if (token) {
      try {
        return await apiClient.execNodeCommand(token, nodeID, normalizedCommand, timeoutSeconds);
      } catch (error) {
        handleWriteApiError("节点命令执行", error);
        return { ok: false, message: "命令执行请求失败", output: "", exitCode: -1, durationMs: 0 };
      }
    } else {
      ensureDemoWriteAllowed("节点命令执行");
    }

    const now = new Date().toLocaleString("zh-CN", { hour12: false });
    const output = [
      `$ ${normalizedCommand}`,
      `节点 #${nodeID} 已执行命令`,
      `执行时间 ${now}`
    ].join("\n");

    return {
      ok: true,
      message: "命令执行成功",
      output,
      exitCode: 0,
      durationMs: 120
    };
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const createTask = useCallback(async (input: NewTaskInput): Promise<number> => {
    if (token) {
      try {
        const created = await apiClient.createTask(token, input);
        setTasks((prev) => [created, ...prev]);
        return created.id;
      } catch (error) {
        handleWriteApiError("创建任务", error);
        return -1;
      }
    } else {
      ensureDemoWriteAllowed("创建任务");
    }

    const node = nodes.find((item) => item.id === input.nodeId);
    const policy = input.policyId ? policies.find((item) => item.id === input.policyId) : undefined;
    const nextTaskID = tasks.length > 0 ? Math.max(...tasks.map((task) => task.id)) + 1 : 3001;

    const nextTask: TaskRecord = {
      id: nextTaskID,
      name: input.name,
      policyName: policy?.name ?? input.name,
      policyId: policy?.id ?? input.policyId ?? null,
      nodeName: node?.name ?? `节点-${input.nodeId}`,
      nodeId: input.nodeId,
      status: "pending",
      progress: 0,
      startedAt: "-",
      command: input.command,
      rsyncSource: input.rsyncSource ?? policy?.sourcePath,
      rsyncTarget: input.rsyncTarget ?? policy?.targetPath,
      executorType: input.executorType ?? ((input.rsyncSource || input.rsyncTarget || policy) ? "rsync" : "local"),
      cronSpec: input.cronSpec ?? policy?.cron,
      speedMbps: 0
    };

    setTasks((prev) => [nextTask, ...prev]);
    return nextTaskID;
  }, [ensureDemoWriteAllowed, handleWriteApiError, nodes, policies, tasks, token]);

  const deleteTask = useCallback(async (taskID: number) => {
    if (token) {
      try {
        await apiClient.deleteTask(token, taskID);
      } catch (error) {
        handleWriteApiError("删除任务", error);
      }
    } else {
      ensureDemoWriteAllowed("删除任务");
    }

    setTasks((prev) => prev.filter((task) => task.id !== taskID));
    setAlerts((prev) => prev.filter((alert) => alert.taskId !== taskID));
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const triggerTask = useCallback(async (taskID: number) => {
    if (token) {
      try {
        await apiClient.triggerTask(token, taskID);
        const latest = await apiClient.getTask(token, taskID).catch(() => null);
        setTasks((prev) =>
          prev.map((task) =>
            task.id === taskID
              ? latest ?? {
                  ...task,
                  status: "running",
                  progress: 12,
                  errorCode: undefined,
                  lastError: undefined,
                  startedAt: new Date().toLocaleString("zh-CN", { hour12: false })
                }
              : task
          )
        );
        return;
      } catch (error) {
        handleWriteApiError("触发任务", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("触发任务");
    }

    setTasks((prev) =>
      prev.map((task) =>
        task.id === taskID
          ? {
              ...task,
              status: "running",
              progress: 12,
              errorCode: undefined,
              lastError: undefined,
              startedAt: new Date().toLocaleString("zh-CN", { hour12: false })
            }
          : task
        )
    );
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const cancelTask = useCallback(async (taskID: number) => {
    if (token) {
      try {
        await apiClient.cancelTask(token, taskID);
        const latest = await apiClient.getTask(token, taskID).catch(() => null);
        setTasks((prev) =>
          prev.map((task) =>
            task.id === taskID
              ? latest ?? {
                  ...task,
                  status: "canceled",
                  progress: 0,
                  speedMbps: 0
                }
              : task
          )
        );
        return;
      } catch (error) {
        handleWriteApiError("取消任务", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("取消任务");
    }

    setTasks((prev) =>
      prev.map((task) =>
        task.id === taskID
          ? {
              ...task,
              status: "canceled",
              progress: 0,
              speedMbps: 0
            }
          : task
        )
    );
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const retryTask = useCallback(async (taskID: number) => {
    await triggerTask(taskID);

    const relatedAlerts = alerts.filter((alert) => alert.taskId === taskID && alert.status !== "resolved");
    if (token && relatedAlerts.length > 0) {
      void Promise.allSettled(relatedAlerts.map((alert) => apiClient.resolveAlert(token, alert.id)));
    }

    setAlerts((prev) =>
      prev.map((alert) =>
        alert.taskId === taskID
          ? {
              ...alert,
              status: "resolved",
              retryable: false,
              message: "已触发重试，等待任务结果回传"
            }
          : alert
      )
    );
  }, [alerts, token, triggerTask]);

  const fetchTaskLogs = useCallback(async (taskID: number, options?: { beforeId?: number; limit?: number }): Promise<LogEvent[]> => {
    if (token) {
      try {
        return await apiClient.getTaskLogs(token, taskID, options);
      } catch (error) {
        setWarning((error as Error).message);
        return [];
      }
    }

    return [];
  }, [token]);

  const createPolicy = useCallback(async (input: NewPolicyInput) => {
    if (token) {
      try {
        const created = await apiClient.createPolicy(token, input);
        const merged = {
          ...created,
          criticalThreshold: input.criticalThreshold,
          naturalLanguage: describeCron(created.cron)
        };
        setPolicies((prev) => [merged, ...prev]);
        return;
      } catch (error) {
        handleWriteApiError("创建策略", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("创建策略");
    }

    const nextID = policies.length > 0 ? Math.max(...policies.map((policy) => policy.id)) + 1 : 1;
    const next: PolicyRecord = {
      id: nextID,
      name: input.name,
      sourcePath: input.sourcePath,
      targetPath: input.targetPath,
      cron: input.cron,
      naturalLanguage: describeCron(input.cron),
      enabled: input.enabled,
      criticalThreshold: Math.max(1, input.criticalThreshold)
    };
    setPolicies((prev) => [next, ...prev]);
  }, [ensureDemoWriteAllowed, handleWriteApiError, policies, token]);

  const updatePolicy = useCallback(async (policyID: number, input: NewPolicyInput) => {
    if (token) {
      try {
        const updated = await apiClient.updatePolicy(token, policyID, input);
        const merged = {
          ...updated,
          criticalThreshold: input.criticalThreshold,
          naturalLanguage: describeCron(updated.cron)
        };
        setPolicies((prev) => prev.map((policy) => (policy.id === policyID ? merged : policy)));
        return;
      } catch (error) {
        handleWriteApiError("更新策略", error);
        return;
      }
    } else {
      ensureDemoWriteAllowed("更新策略");
    }

    setPolicies((prev) =>
      prev.map((policy) =>
        policy.id === policyID
          ? {
              ...policy,
              name: input.name,
              sourcePath: input.sourcePath,
              targetPath: input.targetPath,
              cron: input.cron,
              naturalLanguage: describeCron(input.cron),
              enabled: input.enabled,
              criticalThreshold: Math.max(1, input.criticalThreshold)
            }
          : policy
      )
    );
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const deletePolicy = useCallback(async (policyID: number) => {
    if (token) {
      try {
        await apiClient.deletePolicy(token, policyID);
      } catch (error) {
        handleWriteApiError("删除策略", error);
      }
    } else {
      ensureDemoWriteAllowed("删除策略");
    }

    const policyName = policies.find((policy) => policy.id === policyID)?.name;
    setPolicies((prev) => prev.filter((policy) => policy.id !== policyID));
    if (policyName) {
      setTasks((prev) => prev.filter((task) => task.policyName !== policyName));
      setAlerts((prev) => prev.filter((alert) => alert.policyName !== policyName));
    }
  }, [ensureDemoWriteAllowed, handleWriteApiError, policies, token]);

  const togglePolicy = useCallback(async (policyID: number) => {
    const current = policies.find((policy) => policy.id === policyID);
    if (!current) {
      return;
    }

    const input: NewPolicyInput = {
      name: current.name,
      sourcePath: current.sourcePath,
      targetPath: current.targetPath,
      cron: current.cron,
      criticalThreshold: current.criticalThreshold,
      enabled: !current.enabled
    };

    await updatePolicy(policyID, input);
  }, [policies, updatePolicy]);

  const updatePolicySchedule = useCallback(async (policyID: number, cron: string, naturalLanguage: string) => {
    const current = policies.find((policy) => policy.id === policyID);
    if (!current) {
      return;
    }
    await updatePolicy(policyID, {
      name: current.name,
      sourcePath: current.sourcePath,
      targetPath: current.targetPath,
      criticalThreshold: current.criticalThreshold,
      enabled: current.enabled,
      cron
    });

    setPolicies((prev) =>
      prev.map((policy) =>
        policy.id === policyID
          ? {
              ...policy,
              naturalLanguage
            }
          : policy
      )
    );
  }, [policies, updatePolicy]);

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
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

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
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

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
  }, [ensureDemoWriteAllowed, handleWriteApiError, integrations, token]);

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
    [alerts, retryTask]
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
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

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
  }, [ensureDemoWriteAllowed, handleWriteApiError, token]);

  const fetchAlertDeliveries = useCallback(async (alertID: string): Promise<AlertDeliveryRecord[]> => {
    if (token) {
      try {
        return await apiClient.getAlertDeliveries(token, alertID);
      } catch (error) {
        setWarning((error as Error).message);
        return [];
      }
    }
    return [];
  }, [token]);

  const fetchAlertDeliveryStats = useCallback(async (hours = 24): Promise<AlertDeliveryStats> => {
    const normalizedHours = Number.isFinite(hours) && hours > 0 ? Math.floor(hours) : 24;

    if (token) {
      try {
        return await apiClient.getAlertDeliveryStats(token, {
          hours: normalizedHours
        });
      } catch (error) {
        setWarning((error as Error).message);
      }
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
    if (token) {
      try {
        return await apiClient.retryAlertDelivery(token, alertID, integrationID);
      } catch (error) {
        handleWriteApiError("重发通知", error);
        return { ok: false, message: "重发失败", delivery: { id: "", alertId: alertID, integrationId: integrationID, status: "failed", createdAt: "-" } };
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
        return { ok: false, message: "批量重发失败", totalFailed: 0, successCount: 0, failedCount: 0, newDeliveries: [] };
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
    execNodeCommand,

    createPolicy,
    updatePolicy,
    deletePolicy,
    createTask,
    deleteTask,
    triggerTask,
    cancelTask,
    retryTask,
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
