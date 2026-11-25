import { useCallback, useLayoutEffect, useRef, type Dispatch, type SetStateAction } from "react";
import { useAuth } from "@/context/auth-context.hooks";
import i18n from "@/i18n";
import { assertSensitiveStepUpReady } from "@/lib/sensitive-step-up";
import { apiClient } from "@/lib/api/client";
import { formatTime, getAuthSessionGeneration, isAuthTransitionActive } from "@/lib/api/core";
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

function attemptIsCurrent(isCurrent?: () => boolean) {
  return isCurrent?.() !== false;
}

function latestActivityMs(...values: Array<number | undefined>): number | undefined {
  let best = 0;
  for (const value of values) {
    if (typeof value === "number" && Number.isFinite(value) && value > best) {
      best = value;
    }
  }
  return best > 0 ? best : undefined;
}

// The task PUT response omits the latest-terminal projection. Keep the loaded
// pair for the same node until the next read; a node change starts from what the
// server returned, and a returned pair always replaces the old one whole.
function retainLoadedCompletion(previous: TaskRecord, updated: TaskRecord): TaskRecord {
  if (
    updated.nodeId !== previous.nodeId
    || updated.lastFinishedAt !== undefined
    || previous.lastFinishedAt === undefined
  ) {
    return updated;
  }
  return {
    ...updated,
    lastFinishedAt: previous.lastFinishedAt,
    lastDurationMs: previous.lastDurationMs,
    lastActivityMs: latestActivityMs(previous.lastActivityMs, updated.lastActivityMs),
  };
}

// Canonical task revisions are positive UnixNano decimals (see safeTaskRevision);
// anything else is unknown and never orders two rows.
const CANONICAL_TASK_REVISION = /^[1-9]\d*$/;

function isStaleSave(candidate: string | undefined, loaded: string | undefined, expected: string): boolean {
  // The successful compare-and-set supersedes its exact predecessor even if
  // the server's wall clock moved backward when it stamped the new revision.
  if (
    candidate === undefined
    || loaded === undefined
    || loaded === expected
    || !CANONICAL_TASK_REVISION.test(candidate)
    || !CANONICAL_TASK_REVISION.test(loaded)
  ) {
    return false;
  }
  return BigInt(candidate) < BigInt(loaded);
}

type UseTaskOperationsParams = {
  token: string | null;
  demoModeEnabled: boolean;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks: TaskRecord[];
  setTasks: Dispatch<SetStateAction<TaskRecord[]>>;
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
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
  markTasksMutated,
  ensureDemoWriteAllowed,
  handleWriteApiError
}: UseTaskOperationsParams) {
  const { totpEnabled } = useAuth();
  const exec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError });
  // Latest rendered rows; read only after an await, never during render.
  const tasksRef = useRef(tasks);
  useLayoutEffect(() => {
    tasksRef.current = tasks;
  }, [tasks]);
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
    const sessionGeneration = getAuthSessionGeneration();
    const sessionCurrent = () => !isAuthTransitionActive() && getAuthSessionGeneration() === sessionGeneration;
    const result = await exec(
      i18n.t("tasks.actions.updateTask"),
      (t) => apiClient.updateTask(t, taskID, input),
      sessionCurrent,
    );
    if (result) {
      if (result.ok) {
        // A save that lands after its session retired belongs to no current view.
        if (!sessionCurrent()) return;
        const updated = result.data;
        const loaded = tasksRef.current.find((task) => task.id === taskID);
        // The rendered row only decides whether to invalidate an in-flight list
        // read and whether to read completion; the row itself is resolved against
        // the live state by revision.
        const loadedIsNewer = loaded !== undefined && isStaleSave(updated.revision, loaded.revision, input.expectedRevision);
        if (!loadedIsNewer) {
          markTasksMutated();
        }
        // A save response older than the row now loaded (a later save, refresh or
        // list read already landed) must not roll that row back, node and
        // completion included.
        setTasks((prev) => prev.map((task) => {
          if (task.id !== taskID) return task;
          return isStaleSave(updated.revision, task.revision, input.expectedRevision) ? task : retainLoadedCompletion(task, updated);
        }));
        if (
          token
          && loaded
          && !loadedIsNewer
          && updated.lastFinishedAt === undefined
          && (loaded.lastFinishedAt === undefined || loaded.nodeId !== updated.nodeId)
        ) {
          // The PUT row carries no completion and the row had none to keep (never
          // finished, moved, or still awaiting an earlier save's read). Read the
          // task and hydrate only the row this save produced, so any later edit,
          // list load, refresh, delete or session change wins.
          void apiClient.getTask(token, taskID).then((latest) => {
            if (
              !sessionCurrent()
              || latest.id !== taskID
              || latest.nodeId !== updated.nodeId
              || latest.lastFinishedAt === undefined
            ) return;
            setTasks((prev) => prev.map((task) => (task === updated
              ? {
                  ...updated,
                  lastFinishedAt: latest.lastFinishedAt,
                  lastDurationMs: latest.lastDurationMs,
                  lastActivityMs: latestActivityMs(updated.lastActivityMs, latest.lastActivityMs),
                }
              : task)));
          }, () => undefined);
        }
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
          if (input.nodeId !== task.nodeId) {
            next.lastFinishedAt = undefined;
            next.lastDurationMs = undefined;
          }
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
  }, [exec, markTasksMutated, nodes, policies, setTasks, token]);

  const deleteTask = useCallback(async (taskID: number) => {
    await exec(i18n.t("tasks.actions.deleteTask"), (t) => apiClient.deleteTask(t, taskID));
    markTasksMutated();
    setTasks((prev) => prev.filter((task) => task.id !== taskID));
    setAlerts((prev) => prev.filter((alert) => alert.taskId !== taskID));
  }, [exec, markTasksMutated, setAlerts, setTasks]);

  const triggerTask = useCallback(async (taskID: number, isCurrent?: () => boolean) => {
    if (!attemptIsCurrent(isCurrent)) return;
    const sessionGeneration = token
      ? assertSensitiveStepUpReady({ token, totpEnabled })
      : getAuthSessionGeneration();
    const producerCurrent = () => attemptIsCurrent(isCurrent)
      && !isAuthTransitionActive()
      && getAuthSessionGeneration() === sessionGeneration;
    const result = await exec(i18n.t("tasks.actions.triggerTask"), async (t) => {
      if (!producerCurrent()) return null;
      await withStepUp(async (proof) => {
        if (!producerCurrent()) return;
        try {
          await apiClient.requestTaskManualTriggerCredentialGrant(t, {
            taskId: taskID,
            reason: i18n.t("tasks.manualTriggerGrantReason", { id: taskID }),
            requestedTtlSeconds: 600,
          }, proof);
          if (!producerCurrent()) return;
          await apiClient.triggerTask(t, taskID, proof);
        } catch (error) {
          if (!producerCurrent()) return;
          throw error;
        }
      });
      if (!producerCurrent()) return null;
      const latestTask = await apiClient.getTask(t, taskID).catch(() => null);
      if (!producerCurrent()) return null;
      return latestTask;
    });
    if (!producerCurrent()) return;
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
  }, [exec, markTasksMutated, setTasks, token, totpEnabled, withStepUp]);

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

  const retryTask = useCallback(async (taskID: number, isCurrent?: () => boolean) => {
    await triggerTask(taskID, isCurrent);
    if (!token || !attemptIsCurrent(isCurrent)) {
      return;
    }
    try {
      const alerts = await apiClient.getAlerts(token);
      if (!attemptIsCurrent(isCurrent)) return;
      setAlerts(alerts);
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
      return apiClient.getTaskLogs(token, taskID, options);
    }
    if (demoModeEnabled) {
      const mocks = await import("@/data/mock");
      const rows = mocks.mockSeedLogs
        .filter((log) => log.taskId === taskID)
        .sort((first, second) => Number(second.logId ?? 0) - Number(first.logId ?? 0));
      return rows.slice(0, options?.limit ?? rows.length);
    }
    return [];
  }, [demoModeEnabled, token]);

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
