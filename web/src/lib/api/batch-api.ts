import type { BatchCommandResponseModule } from "./batch-command-response";
import { request } from "./core";

interface BatchCreateResponse {
  batch_id: string;
  task_ids: number[];
  run_ids: number[];
  retain: boolean;
}

type BatchDispatchStatus = "pending" | "dispatching" | "accepted" | "failed";

export interface BatchResult {
  batchId: string;
  taskIds: number[];
  runIds: number[];
  retain: boolean;
}

export type BatchRetryFailureKind = "dispatch" | "execution";

export type BatchRetryReason =
  | "eligible"
  | "legacy"
  | "source_changed"
  | "source_missing"
  | "not_failed"
  | "active"
  | "effects_pending"
  | "history_unverified"
  | "already_retried";

export interface BatchRetrySource {
  taskId: number;
  runId: number;
}

export interface BatchRetryEligibility {
  taskId: number;
  nodeId: number;
  runId: number;
  eligible: boolean;
  reason: BatchRetryReason;
  failureKind: BatchRetryFailureKind | null;
  retryBatchId: string | null;
}

export interface BatchRetryLink {
  taskId: number;
  runId: number;
  batchId: string;
  deleted: boolean;
}

export interface BatchStatus {
  batchId: string;
  total: number;
  statusCounts: Record<string, number>;
  tasks: Array<{
    id: number;
    name: string;
    status: string;
    nodeId: number;
    nodeName: string;
    lastError?: string;
    dispatchStatus?: BatchDispatchStatus;
  }>;
  retain?: boolean;
  sourceBatchId?: string | null;
  retrySources?: BatchRetryEligibility[];
  retryLinks?: BatchRetryLink[];
}

export interface BatchRetryResult extends BatchResult {
  sourceBatchId: string;
}

// crypto/rand.Text is at least 26 base32 characters; the batch id column stops at 64.
const BATCH_ID_PATTERN = /^[A-Z2-7]{26,64}$/;
const RETRY_REQUEST_ERROR = "invalid batch retry request";

function requireIdempotencyKey(key: string): string {
  if (typeof key !== "string" || key.length === 0 || key.length > 256 || /\s/u.test(key)) {
    throw new Error(RETRY_REQUEST_ERROR);
  }
  return key;
}

function serializeRetrySources(sources: readonly BatchRetrySource[]): Array<{ task_id: number; run_id: number }> {
  if (!Array.isArray(sources) || sources.length === 0) throw new Error(RETRY_REQUEST_ERROR);
  const seen = new Set<number>();
  const serialized: Array<{ task_id: number; run_id: number }> = [];
  for (const source of sources) {
    if (typeof source !== "object" || source === null || Array.isArray(source) || !("taskId" in source) || !("runId" in source)) {
      throw new Error(RETRY_REQUEST_ERROR);
    }
    const taskId = source.taskId;
    const runId = source.runId;
    if (
      typeof taskId !== "number" || !Number.isSafeInteger(taskId) || taskId <= 0
      || typeof runId !== "number" || !Number.isSafeInteger(runId) || runId < 0
      || seen.has(taskId)
    ) {
      throw new Error(RETRY_REQUEST_ERROR);
    }
    seen.add(taskId);
    serialized.push({ task_id: taskId, run_id: runId });
  }
  return serialized;
}

let batchCommandResponseModule: Promise<BatchCommandResponseModule> | undefined;

// Call only after `request` has started. Awaiting this chunk first would let
// auth change between the UI's last identity check and the write.
function loadBatchCommandResponse(): Promise<BatchCommandResponseModule> {
  batchCommandResponseModule ??= import("./batch-command-response");
  return batchCommandResponseModule;
}

export function createBatchApi() {
  return {
    async createBatchCommand(
      token: string,
      nodeIds: number[],
      command: string,
      name?: string,
      retain?: boolean,
      stepUpProof?: string,
      idempotencyKey: string = crypto.randomUUID()
    ): Promise<BatchResult> {
      const payload = await request<BatchCreateResponse>("/batch-commands", {
        method: "POST",
        token,
        stepUpProof,
        idempotencyKey,
        body: { node_ids: nodeIds, command, name, retain: retain ?? false },
      });
      return {
        batchId: payload.batch_id,
        taskIds: payload.task_ids ?? [],
        runIds: payload.run_ids ?? [],
        retain: payload.retain ?? false,
      };
    },

    async getBatchStatus(token: string, batchId: string): Promise<BatchStatus> {
      const payload = await request<unknown>(`/batch-commands/${batchId}`, { token });
      const { mapBatchStatus } = await loadBatchCommandResponse();
      return mapBatchStatus(payload);
    },

    async deleteBatch(token: string, batchId: string): Promise<{ deleted: number }> {
      return request<{ deleted: number }>(`/batch-commands/${batchId}`, {
        method: "DELETE",
        token,
      });
    },

    async retryBatchCommand(
      token: string,
      sourceBatchId: string,
      sources: readonly BatchRetrySource[],
      stepUpProof: string | undefined,
      idempotencyKey: string,
    ): Promise<BatchRetryResult> {
      if (typeof sourceBatchId !== "string" || !BATCH_ID_PATTERN.test(sourceBatchId)) throw new Error(RETRY_REQUEST_ERROR);
      const key = requireIdempotencyKey(idempotencyKey);
      const bodySources = serializeRetrySources(sources);
      const payload = await request<unknown>(`/batch-commands/${sourceBatchId}/retry`, {
        method: "POST",
        token,
        stepUpProof,
        idempotencyKey: key,
        body: { sources: bodySources },
      });
      const { parseBatchRetryResult } = await loadBatchCommandResponse();
      return parseBatchRetryResult(payload, sourceBatchId, sources);
    },
  };
}
