import { useCallback, useRef, type Dispatch, type SetStateAction } from "react";
import { apiClient } from "@/lib/api/client";
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

  const refreshTasks = useCallback(async (_options?: { limit?: number; offset?: number }) => {
    if (!token) return;
    refreshTasksAbortRef.current?.abort();
    const controller = new AbortController();
    refreshTasksAbortRef.current = controller;
    const taskVersionAtStart = taskVersionRef.current;
    try {
      const result = await apiClient.getTasks(token);
      if (taskVersionAtStart === taskVersionRef.current) {
        setTasks(result);
      }
    } catch {
      // 按需刷新失败时静默处理
    }
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
