import type { LogEvent, RestoreDrillEvidence, RestoreDrillStatus, TaskRunRecord, TaskStatus } from "@/types/domain";
import { extractErrorCode, formatTime, request, type PaginatedEnvelope, unwrapPaginated } from "./core";

type RestoreDrillEvidenceResponse = {
  id: number;
  policy_id: number;
  task_id: number;
  task_run_id: number;
  source_task_run_id?: number | null;
  snapshot_ref?: string;
  sandbox_node_id: number;
  sandbox_node_name?: string;
  sandbox_path?: string;
  status: string;
  failed_step?: string;
  confidence_eligible?: boolean;
  started_at?: string | null;
  finished_at?: string | null;
  duration_ms?: number;
  restore_status?: string;
  restore_started_at?: string | null;
  restore_finished_at?: string | null;
  restore_error?: string;
  verify_status?: string;
  verify_started_at?: string | null;
  verify_finished_at?: string | null;
  verify_error?: string;
  post_verify_status?: string;
  post_verify_finished_at?: string | null;
  post_verify_error?: string;
  cleanup_status?: string;
  cleanup_started_at?: string | null;
  cleanup_finished_at?: string | null;
  cleanup_error?: string;
  created_at: string;
  updated_at?: string;
};

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
  progress?: number;
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
  drill_evidence?: RestoreDrillEvidenceResponse | null;
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
    case "chain":
    case "drill":
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

function mapDrillStatus(raw?: string): RestoreDrillStatus {
  switch (raw) {
    case "running":
    case "success":
    case "failed":
    case "skipped":
    case "canceled":
      return raw;
    default:
      return "pending";
  }
}

function mapDrillEvidence(row?: RestoreDrillEvidenceResponse | null): RestoreDrillEvidence | null {
  if (!row) return null;
  return {
    id: Number(row.id),
    policyId: Number(row.policy_id),
    taskId: Number(row.task_id),
    taskRunId: Number(row.task_run_id),
    sourceTaskRunId: row.source_task_run_id ?? null,
    snapshotRef: row.snapshot_ref || undefined,
    sandboxNodeId: Number(row.sandbox_node_id),
    sandboxNodeName: row.sandbox_node_name ?? "",
    sandboxPath: row.sandbox_path ?? "",
    status: mapDrillStatus(row.status),
    failedStep: row.failed_step || undefined,
    confidenceEligible: row.confidence_eligible ?? false,
    startedAt: formatTime(row.started_at),
    finishedAt: formatTime(row.finished_at),
    durationMs: row.duration_ms ?? 0,
    restoreStatus: mapDrillStatus(row.restore_status),
    restoreStartedAt: formatTime(row.restore_started_at),
    restoreFinishedAt: formatTime(row.restore_finished_at),
    restoreError: row.restore_error || undefined,
    verifyStatus: mapDrillStatus(row.verify_status),
    verifyStartedAt: formatTime(row.verify_started_at),
    verifyFinishedAt: formatTime(row.verify_finished_at),
    verifyError: row.verify_error || undefined,
    postVerifyStatus: mapDrillStatus(row.post_verify_status),
    postVerifyFinishedAt: formatTime(row.post_verify_finished_at),
    postVerifyError: row.post_verify_error || undefined,
    cleanupStatus: mapDrillStatus(row.cleanup_status),
    cleanupStartedAt: formatTime(row.cleanup_started_at),
    cleanupFinishedAt: formatTime(row.cleanup_finished_at),
    cleanupError: row.cleanup_error || undefined,
    createdAt: formatTime(row.created_at),
    updatedAt: formatTime(row.updated_at),
  };
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
    progress: row.progress ?? 0,
    lastError: row.last_error ?? undefined,
    createdAt: formatTime(row.created_at),
    drillEvidence: mapDrillEvidence(row.drill_evidence),
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
      options?: { page?: number; pageSize?: number; status?: string; signal?: AbortSignal }
    ): Promise<{ items: TaskRunRecord[]; total: number; page: number; pageSize: number }> {
      const query = new URLSearchParams();
      if (options?.page && Number.isFinite(options.page) && options.page > 0) {
        query.set("page", String(options.page));
      }
      if (options?.pageSize && Number.isFinite(options.pageSize) && options.pageSize > 0) {
        query.set("page_size", String(options.pageSize));
      }
      if (options?.status) {
        query.set("status", options.status);
      }
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const payload = await request<PaginatedEnvelope<TaskRunResponse[]>>(
        `/tasks/${taskId}/runs${suffix}`,
        { token, signal: options?.signal }
      );
      const result = unwrapPaginated(payload);
      return {
        items: result.items.map(mapTaskRun),
        total: result.total,
        page: result.page,
        pageSize: result.pageSize,
      };
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
      const rows = (await request<TaskRunLogResponse[]>(
        `/task-runs/${runId}/logs${suffix}`,
        { token }
      )) ?? [];
      return rows.map(mapTaskRunLog);
    },
  };
}
