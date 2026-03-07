import type { LogEvent, NewTaskInput, TaskRecord, TaskStatus } from "@/types/domain";
import { extractErrorCode, formatTime, request, type Envelope, unwrapData } from "./core";

type TaskResponse = {
  id: number;
  name: string;
  status: string;
  command?: string;
  rsync_source?: string;
  rsync_target?: string;
  executor_type?: string;
  cron_spec?: string;
  policy_id?: number | null;
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
      return raw;
    default:
      return "pending";
  }
}

function mapTaskExecutor(raw?: string): TaskRecord["executorType"] {
  return raw === "rsync" ? raw : "rsync";
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

function deriveTaskProgress(status: TaskStatus, retryCount: number, index: number): number {
  switch (status) {
    case "success":
      return 100;
    case "running":
      return 32 + (index * 11) % 52;
    case "retrying":
      return Math.min(95, 18 + retryCount * 15 + (index % 13));
    case "failed":
      return 68;
    case "canceled":
      return 0;
    default:
      return 0;
  }
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
    nodeName: row.node?.name ?? `节点-${row.node_id ?? 0}`,
    nodeId: row.node?.id ?? row.node_id ?? 0,
    createdAt: formatTime(row.created_at),
    status,
    progress: deriveTaskProgress(status, retryCount, index),
    startedAt: formatTime(row.last_run_at ?? row.created_at),
    nextRunAt: formatTime(row.next_run_at),
    errorCode,
    lastError: row.last_error ?? undefined,
    retryCount,
    command: row.command ?? undefined,
    rsyncSource: row.rsync_source ?? undefined,
    rsyncTarget: row.rsync_target ?? undefined,
    executorType: mapTaskExecutor(row.executor_type),
    cronSpec: row.cron_spec ?? undefined,
    updatedAt: formatTime(row.updated_at),
    speedMbps: 0
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
      const payload = await request<Envelope<TaskResponse[]>>("/tasks", { token, signal: options?.signal });
      const rows = unwrapData(payload) ?? [];
      return rows.map((row, index) => mapTask(row, index));
    },

    async getTask(token: string, taskId: number): Promise<TaskRecord> {
      const payload = await request<Envelope<TaskResponse>>(`/tasks/${taskId}`, { token });
      return mapTask(unwrapData(payload), 0);
    },

    async createTask(token: string, input: NewTaskInput): Promise<TaskRecord> {
      const payload = await request<Envelope<TaskResponse>>("/tasks", {
        method: "POST",
        token,
        body: {
          name: input.name,
          node_id: input.nodeId,
          policy_id: input.policyId ?? null,
          rsync_source: input.rsyncSource,
          rsync_target: input.rsyncTarget,
          executor_type: input.executorType,
          cron_spec: input.cronSpec
        }
      });
      return mapTask(unwrapData(payload), 0);
    },

    async updateTask(token: string, taskId: number, input: NewTaskInput): Promise<TaskRecord> {
      const payload = await request<Envelope<TaskResponse>>(`/tasks/${taskId}`, {
        method: "PUT",
        token,
        body: {
          name: input.name,
          node_id: input.nodeId,
          policy_id: input.policyId ?? null,
          rsync_source: input.rsyncSource,
          rsync_target: input.rsyncTarget,
          executor_type: input.executorType,
          cron_spec: input.cronSpec
        }
      });
      return mapTask(unwrapData(payload), 0);
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
      const payload = await request<Envelope<TaskLogResponse[]>>(`/tasks/${taskId}/logs${suffix}`, { token });
      const rows = unwrapData(payload) ?? [];
      return rows.map((row) => mapTaskLog(row));
    },

    async triggerTask(token: string, taskId: number): Promise<void> {
      await request(`/tasks/${taskId}/trigger`, {
        method: "POST",
        token
      });
    },

    async cancelTask(token: string, taskId: number): Promise<void> {
      await request(`/tasks/${taskId}/cancel`, {
        method: "POST",
        token
      });
    }
  };
}
