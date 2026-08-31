import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { apiClient } from "@/lib/api/client";
import i18n from "@/i18n";
import { getErrorMessage } from "@/lib/utils";
import { idleInventoryRequestState, isAbortError, type InventoryRequestState } from "@/hooks/inventory-request-state";
import { useTaskOperations } from "@/hooks/use-console-task-operations";
import type {
  AlertRecord,
  NodeRecord,
  PolicyRecord,
  TaskRecord,
} from "@/types/domain";

export type UseTasksDomainParams = {
  token: string | null;
  demoModeEnabled: boolean;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  alerts: AlertRecord[];
  setTasks: Dispatch<SetStateAction<TaskRecord[]>>;
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  setWarning: Dispatch<SetStateAction<string | null>>;
  taskVersionRef: { current: number };
  markTasksMutated: () => void;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

// 任务域：自持 refreshTasks 与 abort/版本戳控制，接线 useTaskOperations。
export function useTasksDomain({
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
}: UseTasksDomainParams) {
  const refreshTasksAbortRef = useRef<AbortController | null>(null);
  const inFlightRefreshRef = useRef<Promise<void> | null>(null);
  const [tasksRequest, setTasksRequest] = useState<InventoryRequestState>(idleInventoryRequestState);
  const tasksRequestGenRef = useRef(0);

  useEffect(() => {
    return () => {
      refreshTasksAbortRef.current?.abort();
      refreshTasksAbortRef.current = null;
      inFlightRefreshRef.current = null;
    };
  }, [token]);

  const refreshTasks = useCallback(async (_options?: { limit?: number; offset?: number }) => {
    if (!token) {
      setTasksRequest({ loading: false, error: null, loaded: true });
      return;
    }
    if (inFlightRefreshRef.current) {
      return inFlightRefreshRef.current;
    }
    const controller = new AbortController();
    refreshTasksAbortRef.current = controller;
    const requestGen = ++tasksRequestGenRef.current;
    const taskVersionAtStart = taskVersionRef.current;
    setTasksRequest((prev) => ({ ...prev, loading: true }));
    const pending = (async () => {
      try {
        const result = await apiClient.getTasks(token, { signal: controller.signal });
        if (requestGen !== tasksRequestGenRef.current || controller.signal.aborted) {
          return;
        }
        if (taskVersionAtStart === taskVersionRef.current) {
          setTasks(result);
        }
        setTasksRequest({ loading: false, error: null, loaded: true });
      } catch (error) {
        if (requestGen !== tasksRequestGenRef.current || controller.signal.aborted || isAbortError(error)) {
          return;
        }
        setTasksRequest((prev) => ({
          loading: false,
          error: getErrorMessage(error, i18n.t("tasks.loadFailed")),
          loaded: prev.loaded,
        }));
      } finally {
        if (refreshTasksAbortRef.current === controller) {
          inFlightRefreshRef.current = null;
        }
      }
    })();
    inFlightRefreshRef.current = pending;
    return pending;
  }, [token, taskVersionRef, setTasks]);

  const {
    createTask,
    updateTask,
    deleteTask,
    triggerTask,
    cancelTask,
    retryTask,
    pauseTask,
    resumeTask,
    skipNextTask,
    refreshTask,
    fetchTaskLogs,
  } = useTaskOperations({
    token,
    demoModeEnabled,
    nodes,
    policies,
    tasks,
    alerts,
    setTasks,
    setAlerts,
    setWarning,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError,
  });

  return {
    tasks,
    tasksLoading: tasksRequest.loading,
    tasksError: tasksRequest.error,
    tasksLoaded: tasksRequest.loaded,
    refreshTasks,
    createTask,
    updateTask,
    deleteTask,
    triggerTask,
    cancelTask,
    retryTask,
    pauseTask,
    resumeTask,
    skipNextTask,
    refreshTask,
    fetchTaskLogs,
  };
}
