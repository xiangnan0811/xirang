import { useCallback, type Dispatch, type SetStateAction } from "react";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import { formatTime } from "@/lib/api/core";
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
    const result = await exec(i18n.t("tasks.actions.createTask"), (t) => apiClient.createTask(t, input));
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

  const updateTask = useCallback(async (taskID: number, input: NewTaskInput): Promise<void> => {
    const result = await exec(i18n.t("tasks.actions.updateTask"), (t) => apiClient.updateTask(t, taskID, input));
    if (result) {
      if (result.ok) {
        markTasksMutated();
        setTasks((prev) => prev.map((task) => (task.id === taskID ? result.data : task)));
        return;
      }
      throw new Error(i18n.t("tasks.actions.updateTaskFailed"));
    }
    // demo mode fallback: update in-memory (与 buildDemoTask 保持一致的派生逻辑)
    const node = nodes.find((n) => n.id === input.nodeId);
    const policy = input.policyId ? policies.find((p) => p.id === input.policyId) : null;
    markTasksMutated();
    setTasks((prev) =>
      prev.map((task) =>
        task.id === taskID
          ? {
              ...task,
              name: input.name,
              policyName: policy?.name ?? input.name,
              nodeId: input.nodeId,
              nodeName: node?.name ?? i18n.t("common.nodeDefault", { id: input.nodeId }),
              policyId: input.policyId ?? null,
              rsyncSource: input.rsyncSource ?? policy?.sourcePath,
              rsyncTarget: input.rsyncTarget ?? policy?.targetPath,
              executorType: input.executorType ?? "rsync",
              cronSpec: input.cronSpec ?? policy?.cron,
            }
          : task
      )
    );
  }, [exec, markTasksMutated, nodes, policies, setTasks]);

  const deleteTask = useCallback(async (taskID: number) => {
    await exec(i18n.t("tasks.actions.deleteTask"), (t) => apiClient.deleteTask(t, taskID));
    markTasksMutated();
    setTasks((prev) => prev.filter((task) => task.id !== taskID));
    setAlerts((prev) => prev.filter((alert) => alert.taskId !== taskID));
  }, [exec, markTasksMutated, setAlerts, setTasks]);

  const triggerTask = useCallback(async (taskID: number) => {
    const result = await exec(i18n.t("tasks.actions.triggerTask"), async (t) => {
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
              startedAt: formatTime(new Date().toISOString())
            }
          : task
      )
    );
  }, [exec, markTasksMutated, setTasks]);

  const cancelTask = useCallback(async (taskID: number) => {
    const result = await exec(i18n.t("tasks.actions.cancelTask"), async (t) => {
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
              message: i18n.t("tasks.retriedMessage")
            }
          : alert
      )
    );
  }, [alerts, setAlerts, token, triggerTask]);

  const pauseTask = useCallback(async (taskID: number, cancelRunning?: boolean) => {
    const result = await exec(i18n.t("tasks.actions.pauseTask"), async (t) => {
      await apiClient.pauseTask(t, taskID, cancelRunning);
      return apiClient.getTask(t, taskID).catch(() => null);
    });
    if (result && !result.ok) return;

    const latest = result?.ok ? result.data : null;
    markTasksMutated();
    setTasks((prev) =>
      prev.map((task) =>
        task.id === taskID
          ? latest ?? { ...task, enabled: false, skipNext: false, nextRunAt: undefined }
          : task
      )
    );
  }, [exec, markTasksMutated, setTasks]);

  const resumeTask = useCallback(async (taskID: number) => {
    const result = await exec(i18n.t("tasks.actions.resumeTask"), async (t) => {
      await apiClient.resumeTask(t, taskID);
      return apiClient.getTask(t, taskID).catch(() => null);
    });
    if (result && !result.ok) return;

    const latest = result?.ok ? result.data : null;
    markTasksMutated();
    setTasks((prev) =>
      prev.map((task) =>
        task.id === taskID
          ? latest ?? { ...task, enabled: true }
          : task
      )
    );
  }, [exec, markTasksMutated, setTasks]);

  const skipNextTask = useCallback(async (taskID: number) => {
    const result = await exec(i18n.t("tasks.actions.skipNextTask"), async (t) => {
      await apiClient.skipNextTask(t, taskID);
      return apiClient.getTask(t, taskID).catch(() => null);
    });
    if (result && !result.ok) return;

    const latest = result?.ok ? result.data : null;
    markTasksMutated();
    setTasks((prev) =>
      prev.map((task) =>
        task.id === taskID
          ? latest ?? { ...task, skipNext: true }
          : task
      )
    );
  }, [exec, markTasksMutated, setTasks]);

  const refreshTask = useCallback(async (taskID: number) => {
    const result = await exec(i18n.t("tasks.actions.refreshTask"), (t) => apiClient.getTask(t, taskID));
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
        setWarning(getErrorMessage(error, i18n.t("tasks.fetchLogsFailed")));
        return [];
      }
    }
    return [];
  }, [setWarning, token]);

  return {
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
    fetchTaskLogs
  };
}
