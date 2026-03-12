import type { LogEvent, TaskRunRecord, TaskStatus } from "@/types/domain";
import { extractErrorCode, formatTime, request, type Envelope, unwrapData } from "./core";

type TaskRunResponse = {
  id: number;
  task_id: number;
  trigger_type: string;
  status: string;
  started_at?: string | null;
  finished_at?: string | null;
  duration_ms: number;
  verify_status: string;
  throughput_mbps: number;
  last_error?: string;
  created_at: string;
  updated_at?: string;
  task?: {
    id?: number;
    name?: string;
    node_id?: number;
    rsync_source?: string;
    rsync_target?: string;
    executor_type?: string;
  };
};

type TaskRunLogResponse = {
  id: number;
  task_id: number;
  task_run_id?: number;
  level: string;
  message: string;
  created_at: string;
};

function mapRunStatus(raw: string): TaskStatus {
  switch (raw) {
    case "running":
    case "pending":
    case "failed":
    case "success":
    case "canceled":
    case "warning":
      return raw;
    default:
      return "pending";
  }
}

function mapTriggerType(raw: string): TaskRunRecord["triggerType"] {
  switch (raw) {
    case "manual":
    case "cron":
    case "retry":
    case "restore":
      return raw;
    default:
      return "manual";
  }
}

function mapVerifyStatus(raw?: string): TaskRunRecord["verifyStatus"] {
  switch (raw) {
    case "passed":
    case "warning":
    case "failed":
      return raw;
    default:
      return "none";
  }
}

function mapLogLevel(raw?: string): LogEvent["level"] {
  if (raw === "error") return "error";
  if (raw === "warn" || raw === "warning") return "warn";
  return "info";
}

function mapTaskRun(row: TaskRunResponse): TaskRunRecord {
  return {
    id: row.id,
    taskId: row.task_id,
    triggerType: mapTriggerType(row.trigger_type),
    status: mapRunStatus(row.status),
    startedAt: formatTime(row.started_at),
    finishedAt: formatTime(row.finished_at),
    durationMs: row.duration_ms,
    verifyStatus: mapVerifyStatus(row.verify_status),
    throughputMbps: row.throughput_mbps,
    lastError: row.last_error ?? undefined,
    createdAt: formatTime(row.created_at),
  };
}

function mapTaskRunLog(row: TaskRunLogResponse): LogEvent {
  return {
    id: `run-${row.task_run_id ?? row.task_id}-${row.id}`,
    logId: row.id,
    timestamp: formatTime(row.created_at),
    timestampMs: new Date(row.created_at).getTime(),
    level: mapLogLevel(row.level),
    message: row.message,
    taskId: row.task_id,
    taskRunId: row.task_run_id,
    errorCode: extractErrorCode(row.message),
  };
}

export function createTaskRunsApi() {
  return {
    async getTaskRuns(
      token: string,
      taskId: number,
      options?: { limit?: number; offset?: number; status?: string; signal?: AbortSignal }
    ): Promise<{ items: TaskRunRecord[]; total: number }> {
      const query = new URLSearchParams();
      if (options?.limit && Number.isFinite(options.limit) && options.limit > 0) {
        query.set("limit", String(options.limit));
      }
      if (options?.offset && Number.isFinite(options.offset) && options.offset >= 0) {
        query.set("offset", String(options.offset));
      }
      if (options?.status) {
        query.set("status", options.status);
      }
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const payload = await request<{ items: TaskRunResponse[]; total: number }>(
        `/tasks/${taskId}/runs${suffix}`,
        { token, signal: options?.signal }
      );
      const items = (payload.items ?? []).map(mapTaskRun);
      return { items, total: payload.total ?? 0 };
    },

    async getTaskRun(token: string, runId: number): Promise<TaskRunRecord> {
      const payload = await request<TaskRunResponse>(`/task-runs/${runId}`, { token });
      return mapTaskRun(payload);
    },

    async getTaskRunLogs(
      token: string,
      runId: number,
      options?: { beforeId?: number; limit?: number; level?: "info" | "warn" | "error" }
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
      const payload = await request<Envelope<TaskRunLogResponse[]>>(
        `/task-runs/${runId}/logs${suffix}`,
        { token }
      );
      const rows = unwrapData(payload) ?? [];
      return rows.map(mapTaskRunLog);
    },
  };
}
