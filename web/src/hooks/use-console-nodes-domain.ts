import { useCallback, useRef, type Dispatch, type SetStateAction } from "react";
import { apiClient } from "@/lib/api/client";
import { useNodeOperations } from "@/hooks/use-console-node-operations";
import type {
  AlertRecord,
  NodeRecord,
  PolicyRecord,
  SSHKeyRecord,
  TaskRecord,
} from "@/types/domain";

export type UseNodesDomainParams = {
  token: string | null;
  demoModeEnabled: boolean;
  nodes: NodeRecord[];
  setNodes: Dispatch<SetStateAction<NodeRecord[]>>;
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  setTasks: Dispatch<SetStateAction<TaskRecord[]>>;
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  setSSHKeys: Dispatch<SetStateAction<SSHKeyRecord[]>>;
  setWarning: Dispatch<SetStateAction<string | null>>;
  inventoryVersionRef: { current: number };
  markInventoryMutated: () => void;
  markTasksMutated: () => void;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

// 节点 + SSH 密钥域：自持 refreshNodes/refreshSSHKeys 与 abort 控制，
// 接线 useNodeOperations（其跨域只读仍走透传值，未引入 ref 环）。
export function useNodesDomain({
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
}: UseNodesDomainParams) {
  const refreshNodesAbortRef = useRef<AbortController | null>(null);
  const refreshSSHKeysAbortRef = useRef<AbortController | null>(null);

  const refreshNodes = useCallback(async (_options?: { limit?: number; offset?: number }) => {
    if (!token) return;
    refreshNodesAbortRef.current?.abort();
    const controller = new AbortController();
    refreshNodesAbortRef.current = controller;
    const inventoryVersionAtStart = inventoryVersionRef.current;
    try {
      const result = await apiClient.getNodes(token, { signal: controller.signal });
      if (inventoryVersionAtStart === inventoryVersionRef.current) {
        setNodes(result);
      }
    } catch {
      // 按需刷新失败时静默处理，不覆盖全局 warning
    }
  }, [token, inventoryVersionRef, setNodes]);

  const refreshSSHKeys = useCallback(async () => {
    if (!token) return;
    refreshSSHKeysAbortRef.current?.abort();
    const controller = new AbortController();
    refreshSSHKeysAbortRef.current = controller;
    const inventoryVersionAtStart = inventoryVersionRef.current;
    try {
      const result = await apiClient.getSSHKeys(token);
      if (inventoryVersionAtStart === inventoryVersionRef.current) {
        setSSHKeys(result);
      }
    } catch {
      // 按需刷新失败时静默处理
    }
  }, [token, inventoryVersionRef, setSSHKeys]);

  const {
    createSSHKey,
    updateSSHKey,
    deleteSSHKey,
    createNode,
    updateNode,
    deleteNode,
    deleteNodes,
    testNodeConnection,
    triggerNodeBackup,
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
    handleWriteApiError,
  });

  return {
    nodes,
    refreshNodes,
    refreshSSHKeys,
    createNode,
    updateNode,
    deleteNode,
    deleteNodes,
    testNodeConnection,
    triggerNodeBackup,
    createSSHKey,
    updateSSHKey,
    deleteSSHKey,
  };
}
