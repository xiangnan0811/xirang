import { useCallback, type Dispatch, type SetStateAction } from "react";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import { formatTime } from "@/lib/api/core";
import { getErrorMessage } from "@/lib/utils";
import { useApiAction } from "@/hooks/use-api-action";
import { useStepUpAction } from "@/hooks/use-step-up-action";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type {
  AlertRecord,
  LogEvent,
  NewTaskInput,
  UpdateTaskInput,
  NodeRecord,
  PolicyRecord,
  TaskRecord
} from "@/types/domain";

type UseTaskOperationsParams = {
  token: string | null;
  demoModeEnabled: boolean;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  setTasks: Dispatch<SetStateAction<TaskRecord[]>>;
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  setWarning: Dispatch<SetStateAction<string | null>>;
  markTasksMutated: () => void;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

export function useTaskOperations({
  token,
  demoModeEnabled,
  nodes,
  policies,
  tasks,
  setTasks,
  setAlerts,
  setWarning,
  markTasksMutated,
  ensureDemoWriteAllowed,
  handleWriteApiError
}: UseTaskOperationsParams) {
  const exec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError });
  const withStepUp = useStepUpAction(
    STEP_UP_ACTIONS.taskManualTrigger,
    { persist: false, reuseCached: false },
  );

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
    // Demo builders must not enter the production startup chunk (same as mock.ts).
    const { buildDemoTask } = await import("@/hooks/use-console-data.demo");
    const nextTask = buildDemoTask(input, nodes, policies, tasks);
    markTasksMutated();
    setTasks((prev) => [nextTask, ...prev]);
    return nextTask.id;
  }, [exec, markTasksMutated, nodes, policies, setTasks, tasks]);

  const updateTask = useCallback(async (taskID: number, input: UpdateTaskInput): Promise<void> => {
    const result = await exec(i18n.t("tasks.actions.updateTask"), (t) => apiClient.updateTask(t, taskID, input));
    if (result) {
      if (result.ok) {
        markTasksMutated();
        setTasks((prev) => prev.map((task) => (task.id === taskID ? result.data : task)));
        return;
      }
      throw new Error(i18n.t("tasks.actions.updateTaskFailed"));
    }
    markTasksMutated();
    setTasks((prev) =>
      prev.map((task) => {
        if (task.id !== taskID) {
          return task;
        }
        const next: TaskRecord = { ...task };
        if (input.name !== undefined) {
          next.name = input.name;
        }
        if (input.nodeId !== undefined) {
          const node = nodes.find((item) => item.id === input.nodeId);
          next.nodeId = input.nodeId;
          next.nodeName = node?.name ?? i18n.t("common.nodeDefault", { id: input.nodeId });
        }
        if (input.policyId !== undefined) {
          next.policyId = input.policyId;
          const policy = input.policyId ? policies.find((item) => item.id === input.policyId) : undefined;
          next.policyName = policy?.name ?? next.name ?? task.policyName;
        }
        if (input.dependsOnTaskId !== undefined) {
          next.dependsOnTaskId = input.dependsOnTaskId;
        }
        if (input.command !== undefined) {
          next.command = input.command;
        }
        if (input.rsyncSource !== undefined) {
          next.rsyncSource = input.rsyncSource;
        }
        if (input.rsyncTarget !== undefined) {
          next.rsyncTarget = input.rsyncTarget;
        }
        if (input.executorType !== undefined) {
          next.executorType = input.executorType;
        }
        if (input.cronSpec !== undefined) {
          next.cronSpec = input.cronSpec;
        }
        if (input.executorSettings !== undefined) {
          if (next.executorType === "restic") {
            const previous = next.executorSettings && "excludePatterns" in next.executorSettings
              ? next.executorSettings
              : { excludePatterns: [], repositoryVersion: null };
            next.executorSettings = {
              excludePatterns: input.executorSettings.excludePatterns ?? previous.excludePatterns,
              repositoryVersion: input.executorSettings.repositoryVersion !== undefined
                ? input.executorSettings.repositoryVersion
                : previous.repositoryVersion,
            };
          } else if (next.executorType === "rclone") {
            const previous = next.executorSettings && "bandwidthLimit" in next.executorSettings
              ? next.executorSettings
              : { bandwidthLimit: "", transfers: 0 };
            next.executorSettings = {
              bandwidthLimit: input.executorSettings.bandwidthLimit ?? previous.bandwidthLimit,
              transfers: input.executorSettings.transfers ?? previous.transfers,
            };
          }
        }
        if (input.executorSecrets?.repositoryPassword && input.executorSecrets.repositoryPassword.trim() !== "") {
          next.executorSecretsConfigured = { repositoryPassword: true };
        }
        return next;
      })
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
      await withStepUp(async (proof) => {
        await apiClient.requestTaskManualTriggerCredentialGrant(t, {
          taskId: taskID,
          reason: i18n.t("tasks.manualTriggerGrantReason", { id: taskID }),
          requestedTtlSeconds: 600,
        }, proof);
        return apiClient.triggerTask(t, taskID, proof);
      });
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
  }, [exec, markTasksMutated, setTasks, withStepUp]);

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
    if (!token) {
      return;
    }
    try {
      setAlerts(await apiClient.getAlerts(token));
    } catch {
      // Keep last known open/acked alerts. Retry must not invent resolved.
    }
  }, [setAlerts, token, triggerTask]);

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
    if (demoModeEnabled) {
      const mocks = await import("@/data/mock");
      const rows = mocks.mockSeedLogs
        .filter((log) => log.taskId === taskID)
        .sort((first, second) => Number(second.logId ?? 0) - Number(first.logId ?? 0));
      return rows.slice(0, options?.limit ?? rows.length);
    }
    return [];
  }, [demoModeEnabled, setWarning, token]);

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
