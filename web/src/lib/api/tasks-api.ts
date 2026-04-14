import type { LogEvent, NewTaskInput, TaskRecord, TaskStatus } from "@/types/domain";
import i18n from "@/i18n";
import { extractErrorCode, formatTime, request } from "./core";

type TaskResponse = {
  id: number;
  name: string;
  status: string;
  command?: string;
  rsync_source?: string;
  rsync_target?: string;
  executor_type?: string;
  executor_config?: string;
  cron_spec?: string;
  policy_id?: number | null;
  depends_on_task_id?: number | null;
  retry_count?: number;
  last_error?: string;
  node_id?: number;
  node?: {
    id?: number;
    name?: string;
  };
  policy?: {
    id?: number;
    name?: string;
  };
  last_run_at?: string | null;
  next_run_at?: string | null;
  created_at?: string;
  updated_at?: string;
  source?: string;
  verify_status?: string;
  enabled?: boolean;
  skip_next?: boolean;
  progress?: number;
};

type TaskLogResponse = {
  id: number;
  task_id: number;
  level: string;
  message: string;
  created_at: string;
};

function mapTaskStatus(raw: string): TaskStatus {
  switch (raw) {
    case "running":
    case "pending":
    case "failed":
    case "success":
    case "retrying":
    case "canceled":
    case "warning":
    case "skipped":
      return raw;
    default:
      return "pending";
  }
}

function mapVerifyStatus(raw?: string): TaskRecord["verifyStatus"] {
  switch (raw) {
    case "passed":
    case "warning":
    case "failed":
      return raw;
    default:
      return "none";
  }
}

function mapTaskExecutor(raw?: string): TaskRecord["executorType"] {
  switch (raw) {
    case "command": return "command";
    case "restic":  return "restic";
    case "rclone":  return "rclone";
    default:        return "rsync";
  }
}

function mapLogLevel(raw?: string): LogEvent["level"] {
  if (raw === "error") {
    return "error";
  }
  if (raw === "warn" || raw === "warning") {
    return "warn";
  }
  return "info";
}

/** @internal 仅导出用于测试 */
export function deriveTaskProgress(
  status: TaskStatus,
  _retryCount: number,
  _index: number,
  apiProgress?: number,
): number {
  // 后端返回了进度字段时直接使用（包含 0，表示有活跃 run 但尚无进度样本）
  if (apiProgress != null) return apiProgress;
  if (status === "success" || status === "warning") return 100;
  if (status === "canceled" || status === "pending" || status === "skipped") return 0;
  // running/retrying 无进度数据时显示 0（不再使用虚假值）
  return 0;
}

function mapTask(row: TaskResponse, index: number): TaskRecord {
  const status = mapTaskStatus(row.status);
  const retryCount = row.retry_count ?? 0;
  const errorCode = status === "failed" ? extractErrorCode(row.last_error) : undefined;

  return {
    id: row.id,
    name: row.name,
    policyName: row.policy?.name ?? row.name,
    policyId: row.policy?.id ?? row.policy_id ?? null,
    nodeName: row.node?.name ?? i18n.t("common.nodeDefault", { id: row.node_id ?? 0 }),
    nodeId: row.node?.id ?? row.node_id ?? 0,
    dependsOnTaskId: row.depends_on_task_id ?? null,
    createdAt: formatTime(row.created_at),
    status,
    progress: deriveTaskProgress(status, retryCount, index, row.progress),
    hasActiveRun: row.progress != null,
    startedAt: formatTime(row.last_run_at ?? row.created_at),
    nextRunAt: formatTime(row.next_run_at),
    errorCode,
    lastError: row.last_error ?? undefined,
    retryCount,
    command: row.command ?? undefined,
    rsyncSource: row.rsync_source ?? undefined,
    rsyncTarget: row.rsync_target ?? undefined,
    executorType: mapTaskExecutor(row.executor_type),
    executorConfig: row.executor_config ?? undefined,
    cronSpec: row.cron_spec ?? undefined,
    updatedAt: formatTime(row.updated_at),
    speedMbps: 0,
    source: row.source ?? "manual",
    verifyStatus: mapVerifyStatus(row.verify_status),
    enabled: row.enabled !== false,
    skipNext: row.skip_next === true,
  };
}

function mapTaskLog(row: TaskLogResponse): LogEvent {
  return {
    id: `history-${row.task_id}-${row.id}`,
    logId: row.id,
    timestamp: formatTime(row.created_at),
    timestampMs: new Date(row.created_at).getTime(),
    level: mapLogLevel(row.level),
    message: row.message,
    taskId: row.task_id,
    errorCode: extractErrorCode(row.message)
  };
}

export function createTasksApi() {
  return {
    async getTasks(token: string, options?: { signal?: AbortSignal }): Promise<TaskRecord[]> {
      const rows = (await request<TaskResponse[]>("/tasks", { token, signal: options?.signal })) ?? [];
      return rows.map((row, index) => mapTask(row, index));
    },

    async getTask(token: string, taskId: number): Promise<TaskRecord> {
      const row = await request<TaskResponse>(`/tasks/${taskId}`, { token });
      return mapTask(row, 0);
    },

    async createTask(token: string, input: NewTaskInput): Promise<TaskRecord> {
      const row = await request<TaskResponse>("/tasks", {
        method: "POST",
        token,
        body: {
          name: input.name,
          node_id: input.nodeId,
          policy_id: input.policyId ?? null,
          depends_on_task_id: input.dependsOnTaskId ?? null,
          command: input.command,
          rsync_source: input.rsyncSource,
          rsync_target: input.rsyncTarget,
          executor_type: input.executorType,
          executor_config: input.executorConfig,
          cron_spec: input.cronSpec
        }
      });
      return mapTask(row, 0);
    },

    async updateTask(token: string, taskId: number, input: NewTaskInput): Promise<TaskRecord> {
      const row = await request<TaskResponse>(`/tasks/${taskId}`, {
        method: "PUT",
        token,
        body: {
          name: input.name,
          node_id: input.nodeId,
          policy_id: input.policyId ?? null,
          depends_on_task_id: input.dependsOnTaskId ?? null,
          command: input.command,
          rsync_source: input.rsyncSource,
          rsync_target: input.rsyncTarget,
          executor_type: input.executorType,
          executor_config: input.executorConfig,
          cron_spec: input.cronSpec
        }
      });
      return mapTask(row, 0);
    },

    async deleteTask(token: string, taskId: number): Promise<void> {
      await request(`/tasks/${taskId}`, {
        method: "DELETE",
        token
      });
    },

    async getTaskLogs(
      token: string,
      taskId: number,
      options?: {
        beforeId?: number;
        limit?: number;
        level?: "info" | "warn" | "error";
      }
    ): Promise<LogEvent[]> {
      const query = new URLSearchParams();
      if (options?.beforeId && Number.isFinite(options.beforeId) && options.beforeId > 0) {
        query.set("before_id", String(options.beforeId));
      }
      if (options?.limit && Number.isFinite(options.limit) && options.limit > 0) {
        query.set("limit", String(options.limit));
      }
      if (options?.level) {
        query.set("level", options.level);
      }
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const rows = (await request<TaskLogResponse[]>(`/tasks/${taskId}/logs${suffix}`, { token })) ?? [];
      return rows.map((row) => mapTaskLog(row));
    },

    async triggerTask(token: string, taskId: number): Promise<{ runId?: number }> {
      const payload = await request<{ message?: string; run_id?: number }>(`/tasks/${taskId}/trigger`, {
        method: "POST",
        token
      });
      return { runId: payload.run_id };
    },

    async cancelTask(token: string, taskId: number): Promise<void> {
      await request(`/tasks/${taskId}/cancel`, {
        method: "POST",
        token
      });
    },

    async restoreTask(token: string, taskId: number, targetPath?: string): Promise<{ runId?: number }> {
      const payload = await request<{ message?: string; run_id?: number }>(`/tasks/${taskId}/restore`, {
        method: "POST",
        token,
        body: targetPath ? { target_path: targetPath } : {}
      });
      return { runId: payload.run_id };
    },

    async batchTriggerTasks(token: string, taskIds: number[]): Promise<{ total: number; successCount: number }> {
      const payload = await request<{ total?: number; success_count?: number }>("/tasks/batch-trigger", {
        method: "POST",
        token,
        body: { task_ids: taskIds }
      });
      return { total: payload.total ?? 0, successCount: payload.success_count ?? 0 };
    },

    async pauseTask(token: string, taskId: number, cancelRunning?: boolean): Promise<void> {
      await request(`/tasks/${taskId}/pause`, {
        method: "POST",
        token,
        body: cancelRunning ? { cancel_running: true } : {}
      });
    },

    async resumeTask(token: string, taskId: number): Promise<void> {
      await request(`/tasks/${taskId}/resume`, {
        method: "POST",
        token
      });
    },

    async skipNextTask(token: string, taskId: number): Promise<void> {
      await request(`/tasks/${taskId}/skip-next`, {
        method: "POST",
        token
      });
    }
  };
}
