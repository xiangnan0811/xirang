import { useCallback, useRef, useState, type Dispatch, type SetStateAction } from "react";
import i18n from "@/i18n";
import { getErrorMessage } from "@/lib/utils";
import { idleInventoryRequestState, isAbortError, type InventoryRequestState } from "@/hooks/inventory-request-state";
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
  const [nodesRequest, setNodesRequest] = useState<InventoryRequestState>(idleInventoryRequestState);
  const nodesRequestGenRef = useRef(0);

  const refreshNodes = useCallback(async (_options?: { limit?: number; offset?: number }) => {
    if (!token) {
      setNodesRequest({ loading: false, error: null, loaded: true });
      return;
    }
    refreshNodesAbortRef.current?.abort();
    const controller = new AbortController();
    refreshNodesAbortRef.current = controller;
    const requestGen = ++nodesRequestGenRef.current;
    const inventoryVersionAtStart = inventoryVersionRef.current;
    setNodesRequest((prev) => ({ ...prev, loading: true }));
    try {
      const result = await apiClient.getNodes(token, { signal: controller.signal });
      if (requestGen !== nodesRequestGenRef.current || controller.signal.aborted) {
        return;
      }
      if (inventoryVersionAtStart === inventoryVersionRef.current) {
        setNodes(result);
      }
      setNodesRequest({ loading: false, error: null, loaded: true });
    } catch (error) {
      if (requestGen !== nodesRequestGenRef.current || controller.signal.aborted || isAbortError(error)) {
        return;
      }
      setNodesRequest((prev) => ({
        loading: false,
        error: getErrorMessage(error, i18n.t("nodes.loadFailed")),
        loaded: prev.loaded,
      }));
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
    nodesLoading: nodesRequest.loading,
    nodesError: nodesRequest.error,
    nodesLoaded: nodesRequest.loaded,
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
