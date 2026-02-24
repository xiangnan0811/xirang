import { useCallback, type Dispatch, type SetStateAction } from "react";
import { ApiError, apiClient } from "@/lib/api/client";
import {
  buildFingerprint,
  createKeyId,
  parseTags
} from "@/hooks/use-console-data.utils";
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
  ensureDemoWriteAllowed,
  handleWriteApiError
}: UseNodeOperationsParams) {
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
  }, [ensureDemoWriteAllowed, handleWriteApiError, setSSHKeys, token]);

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
  }, [ensureDemoWriteAllowed, handleWriteApiError, setSSHKeys, token]);

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
  }, [demoModeEnabled, ensureDemoWriteAllowed, handleWriteApiError, nodes, setSSHKeys, setWarning, token]);

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
  }, [createSSHKey, ensureDemoWriteAllowed, handleWriteApiError, nodes, setNodes, token]);

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
  }, [createSSHKey, ensureDemoWriteAllowed, handleWriteApiError, setNodes, token]);

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
  }, [ensureDemoWriteAllowed, handleWriteApiError, nodes, setAlerts, setNodes, setTasks, token]);

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
  }, [ensureDemoWriteAllowed, handleWriteApiError, setAlerts, setNodes, setTasks, token]);

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
  }, [ensureDemoWriteAllowed, handleWriteApiError, setNodes, token]);

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
    [ensureDemoWriteAllowed, handleWriteApiError, nodes, policies, setNodes, setTasks, tasks, token]
  );

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
