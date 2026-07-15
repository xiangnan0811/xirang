import type {
  LogEvent,
  NewTaskInput,
  RsyncPublicationMode,
  RsyncPublicationReasonCode,
  RsyncPublicationState,
  RsyncPublicationSummary,
  RsyncVersionedPublicationMode,
  RsyncVersioningActivationResult,
  RsyncVersioningEstimateBucket,
  RsyncVersioningMigrationChoice,
  RsyncVersioningPreflightResult,
  RsyncVersioningRollbackPreparationResult,
  TaskRecord,
  TaskStatus,
} from "@/types/domain";
import i18n from "@/i18n";
import { ApiError, extractErrorCode, formatTime, request, type PaginatedEnvelope, unwrapPaginated } from "./core";

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
  rsync_publication?: RawRsyncPublicationSummary;
};

type RawRsyncPublicationSummary = {
  mode?: unknown;
  state?: unknown;
  reason_code?: unknown;
  capability_revision?: unknown;
  task_revision?: unknown;
  seed_full_copy_required?: unknown;
};

type RawRsyncVersioningPreflightResult = {
  preflight_id?: unknown;
  mode?: unknown;
  state?: unknown;
  reason_code?: unknown;
  capability_revision?: unknown;
  expires_at?: unknown;
  capacity_estimate?: unknown;
  inode_estimate?: unknown;
};

type RawRsyncVersioningActivationResult = {
  summary?: RawRsyncPublicationSummary;
  migration_choice?: unknown;
};

type RawRsyncVersioningRollbackPreparationResult = {
  summary?: RawRsyncPublicationSummary;
};

export type RsyncVersioningErrorCode =
  | "feature_disabled"
  | "repository_offline"
  | "repository_disconnected"
  | "provider_unavailable"
  | "provider_operation_timeout"
  | "provider_resource_limit"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "unsupported"
  | "request_failed";

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

function mapRsyncPublicationMode(raw: unknown): RsyncPublicationMode | undefined {
  switch (raw) {
    case "legacy_mutable":
    case "versioned_hardlink":
    case "versioned_full_copy":
      return raw;
    default:
      return undefined;
  }
}

function mapRsyncPublicationState(raw: unknown): RsyncPublicationState | undefined {
  switch (raw) {
    case "legacy":
    case "preflight_required":
    case "ready":
    case "preparing":
    case "verifying":
    case "committed":
    case "failed":
    case "blocked":
    case "rollback_prepared":
      return raw;
    default:
      return undefined;
  }
}

function mapRsyncPublicationReasonCode(raw: unknown): RsyncPublicationReasonCode | undefined {
  switch (raw) {
    case "legacy":
    case "preflight_required":
    case "ready":
    case "preflight_expired":
    case "task_revision_changed":
    case "preflight_mismatch":
    case "root_drift":
    case "unsupported":
    case "admission_blocked":
    case "rollback_prepared":
      return raw;
    default:
      return undefined;
  }
}

function mapRsyncVersioningEstimate(raw: unknown): RsyncVersioningEstimateBucket {
  switch (raw) {
    case "constrained":
    case "available":
      return raw;
    default:
      return "unknown";
  }
}

function safePositiveInteger(raw: unknown, fallback: number): number {
  const value = typeof raw === "number" ? raw : Number(raw);
  return Number.isSafeInteger(value) && value > 0 ? value : fallback;
}

function safeTaskRevision(raw: unknown): string {
  return typeof raw === "string" && /^[1-9]\d*$/.test(raw) ? raw : "";
}

function blockedRsyncPublicationSummary(): RsyncPublicationSummary {
  return {
    mode: "legacy_mutable",
    state: "blocked",
    reasonCode: "unsupported",
    capabilityRevision: 1,
    taskRevision: "",
    seedFullCopyRequired: false,
  };
}

function legacyRsyncPublicationSummary(): RsyncPublicationSummary {
  return {
    mode: "legacy_mutable",
    state: "legacy",
    reasonCode: "legacy",
    capabilityRevision: 1,
    taskRevision: "",
    seedFullCopyRequired: false,
  };
}

function mapRsyncPublicationSummary(raw: unknown, fallbackToLegacy = false): RsyncPublicationSummary {
  if (!raw || typeof raw !== "object") {
    return fallbackToLegacy ? legacyRsyncPublicationSummary() : blockedRsyncPublicationSummary();
  }
  const source = raw as RawRsyncPublicationSummary;
  const mode = mapRsyncPublicationMode(source.mode);
  const state = mapRsyncPublicationState(source.state);
  const reasonCode = mapRsyncPublicationReasonCode(source.reason_code);
  if (!mode || !state || !reasonCode) {
    return blockedRsyncPublicationSummary();
  }
  return {
    mode,
    state,
    reasonCode,
    capabilityRevision: safePositiveInteger(source.capability_revision, 1),
    taskRevision: safeTaskRevision(source.task_revision),
    seedFullCopyRequired: source.seed_full_copy_required === true,
  };
}

function mapRsyncVersionedPublicationMode(raw: unknown): RsyncVersionedPublicationMode | undefined {
  const mode = mapRsyncPublicationMode(raw);
  return mode === "versioned_hardlink" || mode === "versioned_full_copy" ? mode : undefined;
}

function mapRsyncVersioningMigrationChoice(raw: unknown): RsyncVersioningMigrationChoice | undefined {
  return raw === "imported_baseline" || raw === "first_new_point" ? raw : undefined;
}

function mapRsyncVersioningPreflightResult(raw: RawRsyncVersioningPreflightResult): RsyncVersioningPreflightResult {
  const mode = mapRsyncVersionedPublicationMode(raw.mode);
  const state = mapRsyncPublicationState(raw.state);
  const reasonCode = mapRsyncPublicationReasonCode(raw.reason_code);
  const preflightId = typeof raw.preflight_id === "string" && /^[a-f0-9]{32}$/.test(raw.preflight_id) ? raw.preflight_id : "";
  if (!mode || !state || !reasonCode || !preflightId) {
    return {
      preflightId: "",
      mode: "versioned_full_copy",
      state: "blocked",
      reasonCode: "unsupported",
      capabilityRevision: 1,
      expiresAt: "",
      capacityEstimate: "unknown",
      inodeEstimate: "unknown",
    };
  }
  return {
    preflightId,
    mode,
    state,
    reasonCode,
    capabilityRevision: safePositiveInteger(raw.capability_revision, 1),
    expiresAt: typeof raw.expires_at === "string" ? raw.expires_at : "",
    capacityEstimate: mapRsyncVersioningEstimate(raw.capacity_estimate),
    inodeEstimate: mapRsyncVersioningEstimate(raw.inode_estimate),
  };
}

function mapRsyncVersioningActivationResult(raw: RawRsyncVersioningActivationResult): RsyncVersioningActivationResult {
  return {
    summary: mapRsyncPublicationSummary(raw.summary),
    migrationChoice: mapRsyncVersioningMigrationChoice(raw.migration_choice) ?? "first_new_point",
  };
}

function mapRsyncVersioningRollbackPreparationResult(raw: RawRsyncVersioningRollbackPreparationResult): RsyncVersioningRollbackPreparationResult {
  return { summary: mapRsyncPublicationSummary(raw.summary) };
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
  const executorType = mapTaskExecutor(row.executor_type);

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
    executorType,
    executorConfig: row.executor_config ?? undefined,
    cronSpec: row.cron_spec ?? undefined,
    updatedAt: formatTime(row.updated_at),
    speedMbps: 0,
    source: row.source ?? "manual",
    verifyStatus: mapVerifyStatus(row.verify_status),
    rsyncPublication: executorType === "rsync"
      ? mapRsyncPublicationSummary(row.rsync_publication, true)
      : undefined,
    enabled: row.enabled !== false,
    skipNext: row.skip_next === true,
  };
}

function readRsyncVersioningErrorCode(error: unknown): RsyncVersioningErrorCode | undefined {
  if (!(error instanceof ApiError) || !error.detail || typeof error.detail !== "object") {
    return undefined;
  }
  const data = (error.detail as { data?: unknown }).data;
  if (!data || typeof data !== "object") {
    return undefined;
  }
  const reason = (data as { reason?: unknown }).reason;
  if (!reason || typeof reason !== "object") {
    return undefined;
  }
  const code = (reason as { code?: unknown }).code;
  switch (code) {
    case "feature_disabled":
    case "repository_offline":
    case "repository_disconnected":
    case "provider_unavailable":
    case "provider_operation_timeout":
    case "provider_resource_limit":
      return code;
    default:
      return undefined;
  }
}

export function getRsyncVersioningErrorCode(error: unknown): RsyncVersioningErrorCode {
  const capabilityCode = readRsyncVersioningErrorCode(error);
  if (capabilityCode) {
    return capabilityCode;
  }
  if (error instanceof ApiError) {
    switch (error.status) {
      case 403:
        return "forbidden";
      case 404:
        return "not_found";
      case 409:
        return "conflict";
      case 501:
        return "unsupported";
      default:
        return "request_failed";
    }
  }
  return "request_failed";
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
      // 后端 /tasks 返回 paginated envelope，与 /alerts 情况相同，需 unwrap。
      const payload = await request<PaginatedEnvelope<TaskResponse[]>>("/tasks", { token, signal: options?.signal });
      const { items } = unwrapPaginated(payload);
      return items.map((row, index) => mapTask(row, index));
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

    async createRsyncVersioningPreflight(
      token: string,
      taskId: number,
      input: { expectedTaskRevision: string; requestedMode: RsyncVersionedPublicationMode },
    ): Promise<RsyncVersioningPreflightResult> {
      const raw = await request<RawRsyncVersioningPreflightResult>(`/tasks/${taskId}/rsync-versioning/preflights`, {
        method: "POST",
        token,
        body: {
          expected_task_revision: input.expectedTaskRevision,
          requested_mode: input.requestedMode,
        },
      });
      return mapRsyncVersioningPreflightResult(raw);
    },

    async activateRsyncVersioning(
      token: string,
      taskId: number,
      input: { expectedTaskRevision: string; preflightId: string; migrationChoice: RsyncVersioningMigrationChoice },
    ): Promise<RsyncVersioningActivationResult> {
      const raw = await request<RawRsyncVersioningActivationResult>(`/tasks/${taskId}/rsync-versioning/activate`, {
        method: "POST",
        token,
        body: {
          expected_task_revision: input.expectedTaskRevision,
          preflight_id: input.preflightId,
          migration_choice: input.migrationChoice,
        },
      });
      return mapRsyncVersioningActivationResult(raw);
    },

    async prepareRsyncVersioningRollback(
      token: string,
      taskId: number,
      input: { expectedTaskRevision: string },
    ): Promise<RsyncVersioningRollbackPreparationResult> {
      const raw = await request<RawRsyncVersioningRollbackPreparationResult>(`/tasks/${taskId}/rsync-versioning/rollback-preparations`, {
        method: "POST",
        token,
        body: { expected_task_revision: input.expectedTaskRevision },
      });
      return mapRsyncVersioningRollbackPreparationResult(raw);
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

    async triggerTask(token: string, taskId: number, stepUpProof?: string): Promise<{ runId?: number }> {
      const payload = await request<{ message?: string; run_id?: number }>(`/tasks/${taskId}/trigger`, {
        method: "POST",
        token,
        stepUpProof
      });
      return { runId: payload.run_id };
    },

    async cancelTask(token: string, taskId: number): Promise<void> {
      await request(`/tasks/${taskId}/cancel`, {
        method: "POST",
        token
      });
    },

    async restoreTask(token: string, taskId: number, targetPath?: string, stepUpProof?: string): Promise<{ runId?: number }> {
      const payload = await request<{ message?: string; run_id?: number }>(`/tasks/${taskId}/restore`, {
        method: "POST",
        token,
        stepUpProof,
        body: targetPath ? { target_path: targetPath } : {}
      });
      return { runId: payload.run_id };
    },

    async batchTriggerTasks(token: string, taskIds: number[], stepUpProof?: string): Promise<{ total: number; successCount: number }> {
      const payload = await request<{ total?: number; success_count?: number }>("/tasks/batch-trigger", {
        method: "POST",
        token,
        stepUpProof,
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
