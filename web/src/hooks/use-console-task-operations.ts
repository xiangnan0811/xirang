import { useCallback, type Dispatch, type SetStateAction } from "react";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import { useApiAction } from "@/hooks/use-api-action";
import { buildDemoTask } from "@/hooks/use-console-data.demo";
import type {
  AlertRecord,
  LogEvent,
  NewTaskInput,
  NodeRecord,
  PolicyRecord,
  TaskRecord
} from "@/types/domain";

type UseTaskOperationsParams = {
  token: string | null;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  alerts: AlertRecord[];
  setTasks: Dispatch<SetStateAction<TaskRecord[]>>;
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  setWarning: Dispatch<SetStateAction<string | null>>;
  markTasksMutated: () => void;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

export function useTaskOperations({
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
}: UseTaskOperationsParams) {
  const exec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError });

  const createTask = useCallback(async (input: NewTaskInput): Promise<number> => {
    const result = await exec("创建任务", (t) => apiClient.createTask(t, input));
    if (result) {
      if (result.ok) {
        markTasksMutated();
        setTasks((prev) => [result.data, ...prev]);
        return result.data.id;
      }
      return -1;
    }
    const nextTask = buildDemoTask(input, nodes, policies, tasks);
    markTasksMutated();
    setTasks((prev) => [nextTask, ...prev]);
    return nextTask.id;
  }, [exec, markTasksMutated, nodes, policies, setTasks, tasks]);

  const deleteTask = useCallback(async (taskID: number) => {
    await exec("删除任务", (t) => apiClient.deleteTask(t, taskID));
    markTasksMutated();
    setTasks((prev) => prev.filter((task) => task.id !== taskID));
    setAlerts((prev) => prev.filter((alert) => alert.taskId !== taskID));
  }, [exec, markTasksMutated, setAlerts, setTasks]);

  const triggerTask = useCallback(async (taskID: number) => {
    const result = await exec("触发任务", async (t) => {
      await apiClient.triggerTask(t, taskID);
      return apiClient.getTask(t, taskID).catch(() => null);
    });
    if (result && !result.ok) return;

    const latest = result?.ok ? result.data : null;
    markTasksMutated();
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
  }, [exec, markTasksMutated, setTasks]);

  const cancelTask = useCallback(async (taskID: number) => {
    const result = await exec("取消任务", async (t) => {
      await apiClient.cancelTask(t, taskID);
      return apiClient.getTask(t, taskID).catch(() => null);
    });
    if (result && !result.ok) return;

    const latest = result?.ok ? result.data : null;
    markTasksMutated();
    setTasks((prev) =>
      prev.map((task) =>
        task.id === taskID
          ? latest ?? { ...task, status: "canceled", progress: 0, speedMbps: 0 }
          : task
      )
    );
  }, [exec, markTasksMutated, setTasks]);

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
  }, [alerts, setAlerts, token, triggerTask]);

  const refreshTask = useCallback(async (taskID: number) => {
    const result = await exec("刷新任务状态", (t) => apiClient.getTask(t, taskID));
    if (result?.ok) {
      markTasksMutated();
      setTasks((prev) => prev.map((task) => (task.id === taskID ? result.data : task)));
    }
  }, [exec, markTasksMutated, setTasks]);

  const fetchTaskLogs = useCallback(async (taskID: number, options?: { beforeId?: number; limit?: number }): Promise<LogEvent[]> => {
    if (token) {
      try {
        return await apiClient.getTaskLogs(token, taskID, options);
      } catch (error) {
        setWarning(getErrorMessage(error, "获取任务日志失败"));
        return [];
      }
    }
    return [];
  }, [setWarning, token]);

  return {
    createTask,
    deleteTask,
    triggerTask,
    cancelTask,
    retryTask,
    refreshTask,
    fetchTaskLogs
  };
}
