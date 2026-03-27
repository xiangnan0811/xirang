import { useCallback, type Dispatch, type SetStateAction } from "react";
import i18n from "@/i18n";
import { ApiError, apiClient } from "@/lib/api/client";
import { formatTime } from "@/lib/api/core";
import { parseTags } from "@/hooks/use-console-data.utils";
import { useApiAction } from "@/hooks/use-api-action";
import {
  buildDemoBackupTask,
  buildDemoNode,
  buildDemoSSHKey,
  simulateDemoConnection
} from "@/hooks/use-console-data.demo";
import type {
  AlertRecord,
  NewNodeInput,
  NewSSHKeyInput,
  NodeRecord,
  PolicyRecord,
  SSHKeyRecord,
  TaskRecord
} from "@/types/domain";

type UseNodeOperationsParams = {
  token: string | null;
  demoModeEnabled: boolean;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  setNodes: Dispatch<SetStateAction<NodeRecord[]>>;
  setTasks: Dispatch<SetStateAction<TaskRecord[]>>;
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  setSSHKeys: Dispatch<SetStateAction<SSHKeyRecord[]>>;
  setWarning: Dispatch<SetStateAction<string | null>>;
  markInventoryMutated: () => void;
  markTasksMutated: () => void;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

export function useNodeOperations({
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
}: UseNodeOperationsParams) {
  const exec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError });

  const createSSHKey = useCallback(async (input: NewSSHKeyInput): Promise<string> => {
    const result = await exec(i18n.t("nodes.actions.createSSHKey"), (t) => apiClient.createSSHKey(t, input));
    if (result) {
      if (result.ok) {
        markInventoryMutated();
        setSSHKeys((prev) => [result.data, ...prev]);
        return result.data.id;
      }
      return "";
    }
    const item = buildDemoSSHKey(input);
    markInventoryMutated();
    setSSHKeys((prev) => [item, ...prev]);
    return item.id;
  }, [exec, markInventoryMutated, setSSHKeys]);

  const updateSSHKey = useCallback(async (keyId: string, input: NewSSHKeyInput) => {
    const result = await exec(i18n.t("nodes.actions.updateSSHKey"), (t) => apiClient.updateSSHKey(t, keyId, input));
    if (result) {
      if (result.ok) {
        markInventoryMutated();
        setSSHKeys((prev) => prev.map((item) => (item.id === keyId ? result.data : item)));
      }
      return;
    }
    markInventoryMutated();
    setSSHKeys((prev) =>
      prev.map((item) =>
        item.id === keyId
          ? {
              ...item,
              name: input.name,
              username: input.username,
              keyType: input.keyType,
              privateKey: input.privateKey,
              fingerprint: buildDemoSSHKey(input).fingerprint
            }
          : item
      )
    );
  }, [exec, markInventoryMutated, setSSHKeys]);

  // deleteSSHKey 有自定义 ApiError 处理，不使用 exec
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
            setWarning(typeof error.detail === "string" ? error.detail : i18n.t("nodes.actions.deleteSSHKeyFailed"));
          } else {
            const detail = typeof error.detail === "string" ? error.detail : error.message;
            const message = i18n.t("nodes.actions.deleteSSHKeyFailedDetail", { detail });
            setWarning(message);
            throw new Error(message);
          }
        } else if (!demoModeEnabled) {
          handleWriteApiError(i18n.t("nodes.actions.deleteSSHKey"), error);
        }
        if (!demoModeEnabled) {
          return false;
        }
      }
    } else {
      ensureDemoWriteAllowed(i18n.t("nodes.actions.deleteSSHKey"));
    }

    markInventoryMutated();
    setSSHKeys((prev) => prev.filter((item) => item.id !== keyId));
    return true;
  }, [demoModeEnabled, ensureDemoWriteAllowed, handleWriteApiError, markInventoryMutated, nodes, setSSHKeys, setWarning, token]);

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

    const finalInput: NewNodeInput = { ...input, keyId };

    const result = await exec(i18n.t("nodes.actions.createNode"), (t) => apiClient.createNode(t, finalInput));
    if (result) {
      if (result.ok) {
        markInventoryMutated();
        setNodes((prev) => [result.data, ...prev]);
        return result.data.id;
      }
      return -1;
    }
    const nextNode = buildDemoNode(input, nodes, keyId);
    markInventoryMutated();
    setNodes((prev) => [nextNode, ...prev]);
    return nextNode.id;
  }, [createSSHKey, exec, markInventoryMutated, nodes, setNodes]);

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

    const finalInput: NewNodeInput = { ...input, keyId };

    const result = await exec(i18n.t("nodes.actions.updateNode"), (t) => apiClient.updateNode(t, nodeID, finalInput));
    if (result) {
      if (result.ok) {
        markInventoryMutated();
        setNodes((prev) => prev.map((node) => (node.id === nodeID ? result.data : node)));
      }
      return;
    }
    markInventoryMutated();
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
  }, [createSSHKey, exec, markInventoryMutated, setNodes]);

  const deleteNode = useCallback(async (nodeID: number) => {
    await exec(i18n.t("nodes.actions.deleteNode"), (t) => apiClient.deleteNode(t, nodeID));
    markInventoryMutated();
    markTasksMutated();
    setNodes((prev) => prev.filter((node) => node.id !== nodeID));
    setTasks((prev) => prev.filter((task) => task.nodeId !== nodeID));
    setAlerts((prev) => prev.filter((alert) => alert.nodeId !== nodeID));
  }, [exec, markInventoryMutated, markTasksMutated, setAlerts, setNodes, setTasks]);

  const deleteNodes = useCallback(async (nodeIDs: number[]): Promise<{ deleted: number; notFoundIds: number[] }> => {
    const normalized = Array.from(new Set(nodeIDs.filter((item) => Number.isFinite(item) && item > 0)));
    if (!normalized.length) {
      return { deleted: 0, notFoundIds: [] };
    }

    const result = await exec(i18n.t("nodes.actions.deleteNodes"), (t) => apiClient.deleteNodes(t, normalized));
    if (result) {
      if (result.ok) {
        const deletedSet = new Set(normalized.filter((id) => !result.data.notFoundIds.includes(id)));
        markInventoryMutated();
        markTasksMutated();
        setNodes((prev) => prev.filter((node) => !deletedSet.has(node.id)));
        setTasks((prev) => prev.filter((task) => !deletedSet.has(task.nodeId)));
        setAlerts((prev) => prev.filter((alert) => !deletedSet.has(alert.nodeId)));
        return result.data;
      }
      return { deleted: 0, notFoundIds: normalized };
    }

    const deletedSet = new Set(normalized);
    markInventoryMutated();
    markTasksMutated();
    setNodes((prev) => prev.filter((node) => !deletedSet.has(node.id)));
    setTasks((prev) => prev.filter((task) => !deletedSet.has(task.nodeId)));
    setAlerts((prev) => prev.filter((alert) => !deletedSet.has(alert.nodeId)));
    return { deleted: normalized.length, notFoundIds: [] };
  }, [exec, markInventoryMutated, markTasksMutated, setAlerts, setNodes, setTasks]);

  const testNodeConnection = useCallback(async (nodeID: number): Promise<{ ok: boolean; message: string }> => {
    const result = await exec(i18n.t("nodes.actions.testConnection"), (t) => apiClient.testNodeConnection(t, nodeID));
    if (result) {
      if (result.ok) {
        const r = result.data;
        const probeTime = formatTime(new Date().toISOString());
        markInventoryMutated();
        setNodes((prev) =>
          prev.map((node) =>
            node.id === nodeID
              ? {
                  ...node,
                  status: r.ok ? "online" : "offline",
                  lastSeenAt: probeTime,
                  diskProbeAt: probeTime,
                  connectionLatencyMs: r.ok ? r.latency_ms : undefined,
                  diskUsedGb: r.disk_used_gb ?? node.diskUsedGb,
                  diskTotalGb: r.disk_total_gb ?? node.diskTotalGb,
                  diskFreePercent: r.disk_total_gb
                    ? Math.max(
                        1,
                        Math.round(
                          ((r.disk_total_gb - (r.disk_used_gb ?? node.diskUsedGb)) /
                            r.disk_total_gb) *
                            100
                        )
                      )
                    : node.diskFreePercent
                }
              : node
          )
        );
        return { ok: r.ok, message: r.message };
      }
      return { ok: false, message: i18n.t("nodes.probeFailed") };
    }

    // Demo 模式模拟
    await new Promise((resolve) => setTimeout(resolve, 650));
    const sim = simulateDemoConnection(nodeID);
    markInventoryMutated();
    setNodes((prev) => prev.map((node) => (node.id === nodeID ? sim.nodeUpdate(node) : node)));
    return sim.result;
  }, [exec, markInventoryMutated, setNodes]);

  const triggerNodeBackup = useCallback(async (nodeID: number) => {
    const node = nodes.find((item) => item.id === nodeID);
    if (!node) {
      return;
    }

    const targetPolicy = policies.find((policy) => policy.enabled) ?? policies[0];

    const result = await exec(i18n.t("nodes.actions.triggerBackup"), (t) =>
      apiClient.createTask(t, {
        name: i18n.t("nodes.manualBackupName", { name: node.name }),
        nodeId: nodeID,
        policyId: targetPolicy?.id ?? null,
        executorType: "rsync",
        rsyncSource: targetPolicy?.sourcePath,
        rsyncTarget: targetPolicy?.targetPath,
        cronSpec: targetPolicy?.cron
      })
    );
    if (result) {
      if (result.ok) {
        markTasksMutated();
        setTasks((prev) => [result.data, ...prev]);
      }
      return;
    }

    markTasksMutated();
    setTasks((prev) => [buildDemoBackupTask(node, tasks, policies), ...prev]);
    setNodes((prev) =>
      prev.map((item) =>
        item.id === nodeID
          ? { ...item, status: "online", lastSeenAt: i18n.t("common.justNow") }
          : item
      )
    );
  }, [exec, markTasksMutated, nodes, policies, setNodes, setTasks, tasks]);

  return {
    createSSHKey,
    updateSSHKey,
    deleteSSHKey,
    createNode,
    updateNode,
    deleteNode,
    deleteNodes,
    testNodeConnection,
    triggerNodeBackup
  };
}
