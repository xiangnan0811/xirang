import i18n from "@/i18n";
import { request } from "./core";

interface BatchDispatchResponse {
  task_id: number;
  status: "pending" | "dispatching" | "accepted" | "failed";
  last_error?: string;
}

interface BatchCreateResponse {
  batch_id: string;
  task_ids: number[];
  run_ids: number[];
  retain: boolean;
}

interface BatchStatusResponse {
  batch_id: string;
  tasks: Array<{
    id: number;
    name: string;
    status: string;
    node_id: number;
    node?: { id?: number; name?: string };
    last_error?: string;
  }>;
  total: number;
  status_counts: Record<string, number>;
  dispatches?: BatchDispatchResponse[];
  retain?: unknown;
  source_batch_id?: unknown;
  retry_sources?: unknown;
  retry_links?: unknown;
}

export interface BatchResult {
  batchId: string;
  taskIds: number[];
  runIds: number[];
  retain: boolean;
}

export type BatchRetryFailureKind = "dispatch" | "execution";

const BATCH_RETRY_REASONS = [
  "eligible",
  "legacy",
  "source_changed",
  "source_missing",
  "not_failed",
  "active",
  "effects_pending",
  "history_unverified",
  "already_retried",
] as const;

export type BatchRetryReason = (typeof BATCH_RETRY_REASONS)[number];

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
    dispatchStatus?: BatchDispatchResponse["status"];
  }>;
  retain?: boolean;
  sourceBatchId?: string | null;
  retrySources?: BatchRetryEligibility[];
  retryLinks?: BatchRetryLink[];
}

export interface BatchRetryResult extends BatchResult {
  sourceBatchId: string;
}

export class BatchRetryContractError extends Error {
  constructor() {
    super("invalid batch retry response");
    this.name = "BatchRetryContractError";
  }
}

// crypto/rand.Text is at least 26 base32 characters; the batch id column stops at 64.
const BATCH_ID_PATTERN = /^[A-Z2-7]{26,64}$/;
const RETRY_REQUEST_ERROR = "invalid batch retry request";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isPositiveSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function isNonNegativeSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function isBatchId(value: unknown): value is string {
  return typeof value === "string" && BATCH_ID_PATTERN.test(value);
}

function isRetryReason(value: unknown): value is BatchRetryReason {
  return typeof value === "string" && BATCH_RETRY_REASONS.some((reason) => reason === value);
}

function taskNodes(tasks: BatchStatus["tasks"]): Map<number, number> | null {
  const nodes = new Map<number, number>();
  for (const task of tasks) {
    if (!isPositiveSafeInteger(task.id) || !isPositiveSafeInteger(task.nodeId) || nodes.has(task.id)) {
      return null;
    }
    nodes.set(task.id, task.nodeId);
  }
  return nodes;
}

function failureShapeAllowed(
  failureKind: BatchRetryFailureKind | null,
  runId: number,
  eligible: boolean,
  reason: BatchRetryReason,
  retryBatchId: string | null,
): boolean {
  if (failureKind === "execution") return true;
  if (failureKind === "dispatch") return runId === 0;
  if (runId === 0) return true;
  return !eligible && reason === "not_failed" && retryBatchId === null;
}

function operableFailure(failureKind: BatchRetryFailureKind | null, runId: number): boolean {
  return (failureKind === "dispatch" && runId === 0) || (failureKind === "execution" && runId > 0);
}

function parseRetrySource(
  row: unknown,
  nodes: Map<number, number>,
  retain: boolean | undefined,
): BatchRetryEligibility | null {
  if (!isRecord(row) || !Object.hasOwn(row, "failure_kind") || !Object.hasOwn(row, "retry_batch_id")) {
    return null;
  }
  const taskId = row.task_id;
  const nodeId = row.node_id;
  const runId = row.run_id;
  const eligible = row.eligible;
  const reason = row.reason;
  if (!isPositiveSafeInteger(taskId) || !isPositiveSafeInteger(nodeId) || !isNonNegativeSafeInteger(runId)) {
    return null;
  }
  if (nodes.get(taskId) !== nodeId || typeof eligible !== "boolean" || !isRetryReason(reason)) {
    return null;
  }

  let failureKind: BatchRetryFailureKind | null;
  if (row.failure_kind === null) failureKind = null;
  else if (row.failure_kind === "dispatch" || row.failure_kind === "execution") failureKind = row.failure_kind;
  else return null;

  let retryBatchId: string | null;
  if (row.retry_batch_id === null) retryBatchId = null;
  else if (isBatchId(row.retry_batch_id)) retryBatchId = row.retry_batch_id;
  else return null;

  if (!failureShapeAllowed(failureKind, runId, eligible, reason, retryBatchId)) return null;
  if (reason === "already_retried") {
    if (eligible || retryBatchId === null || !operableFailure(failureKind, runId)) return null;
  } else if (retryBatchId !== null) {
    return null;
  } else if (reason === "eligible") {
    if (!eligible || retain === undefined || !operableFailure(failureKind, runId)) return null;
  } else if (eligible) {
    return null;
  }

  return { taskId, nodeId, runId, eligible, reason, failureKind, retryBatchId };
}

function parseRetrySources(
  raw: unknown,
  tasks: BatchStatus["tasks"],
  retain: boolean | undefined,
): BatchRetryEligibility[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const nodes = taskNodes(tasks);
  if (nodes === null || raw.length !== nodes.size) return undefined;
  const seen = new Set<number>();
  const sources: BatchRetryEligibility[] = [];
  for (const row of raw) {
    const source = parseRetrySource(row, nodes, retain);
    if (source === null || seen.has(source.taskId)) return undefined;
    seen.add(source.taskId);
    sources.push(source);
  }
  return sources;
}

function parseRetryLink(row: unknown, nodes: Map<number, number>): BatchRetryLink | null {
  if (!isRecord(row)) return null;
  const taskId = row.task_id;
  const runId = row.run_id;
  const batchId = row.batch_id;
  const deleted = row.deleted;
  if (!isPositiveSafeInteger(taskId) || !nodes.has(taskId) || !isNonNegativeSafeInteger(runId)) return null;
  if (!isBatchId(batchId) || typeof deleted !== "boolean") return null;
  return { taskId, runId, batchId, deleted };
}

function parseRetryLinks(raw: unknown, tasks: BatchStatus["tasks"]): BatchRetryLink[] | undefined {
  if (!Array.isArray(raw)) return undefined;
  const nodes = taskNodes(tasks);
  if (nodes === null) return undefined;
  const seen = new Set<string>();
  const links: BatchRetryLink[] = [];
  for (const row of raw) {
    const link = parseRetryLink(row, nodes);
    if (link === null) return undefined;
    const identity = `${link.taskId}\0${link.runId}\0${link.batchId}`;
    if (seen.has(identity)) return undefined;
    seen.add(identity);
    links.push(link);
  }
  return links;
}

function readRetryMetadata(payload: BatchStatusResponse, tasks: BatchStatus["tasks"], status: BatchStatus): void {
  if (typeof payload.retain === "boolean") status.retain = payload.retain;
  if (payload.source_batch_id === null || isBatchId(payload.source_batch_id)) {
    status.sourceBatchId = payload.source_batch_id;
  }
  if (Array.isArray(payload.retry_sources)) {
    const retrySources = parseRetrySources(payload.retry_sources, tasks, status.retain);
    if (retrySources !== undefined) status.retrySources = retrySources;
  }
  if (Array.isArray(payload.retry_links)) {
    const retryLinks = parseRetryLinks(payload.retry_links, tasks);
    if (retryLinks !== undefined) status.retryLinks = retryLinks;
  }
}

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
    if (!isRecord(source)) throw new Error(RETRY_REQUEST_ERROR);
    const taskId = source.taskId;
    const runId = source.runId;
    if (!isPositiveSafeInteger(taskId) || !isNonNegativeSafeInteger(runId) || seen.has(taskId)) {
      throw new Error(RETRY_REQUEST_ERROR);
    }
    seen.add(taskId);
    serialized.push({ task_id: taskId, run_id: runId });
  }
  return serialized;
}

function parseSafeIdList(value: unknown, minimum: number): number[] | null {
  if (!Array.isArray(value) || value.length === 0) return null;
  const ids: number[] = [];
  for (const item of value) {
    if (typeof item !== "number" || !Number.isSafeInteger(item) || item < minimum) return null;
    ids.push(item);
  }
  return ids;
}

function parseBatchRetryResult(
  payload: unknown,
  sourceBatchId: string,
  sources: readonly BatchRetrySource[],
): BatchRetryResult {
  if (!isRecord(payload) || !isBatchId(payload.batch_id) || payload.batch_id === sourceBatchId) {
    throw new BatchRetryContractError();
  }
  if (payload.source_batch_id !== sourceBatchId || typeof payload.retain !== "boolean") {
    throw new BatchRetryContractError();
  }
  const taskIds = parseSafeIdList(payload.task_ids, 1);
  const runIds = parseSafeIdList(payload.run_ids, 0);
  if (taskIds === null || runIds === null || taskIds.length !== runIds.length) {
    throw new BatchRetryContractError();
  }
  if (new Set(taskIds).size !== taskIds.length) throw new BatchRetryContractError();
  const sourceTaskIds = new Set(sources.map((source) => source.taskId));
  if (taskIds.some((taskId) => sourceTaskIds.has(taskId))) throw new BatchRetryContractError();
  return {
    batchId: payload.batch_id,
    sourceBatchId,
    taskIds,
    runIds,
    retain: payload.retain,
  };
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
      const payload = await request<BatchStatusResponse>(
        `/batch-commands/${batchId}`,
        { token }
      );
      const dispatches = new Map((payload.dispatches ?? []).map((dispatch) => [dispatch.task_id, dispatch]));
      const tasks = (payload.tasks ?? []).map((t) => {
        const dispatch = dispatches.get(t.id);
        const dispatchError = dispatch && dispatch.status !== "accepted" ? dispatch.last_error : undefined;
        return {
          id: t.id,
          name: t.name,
          status: t.status,
          nodeId: t.node?.id ?? t.node_id,
          nodeName: t.node?.name ?? i18n.t("common.nodeDefault", { id: t.node_id }),
          lastError: t.last_error || dispatchError,
          dispatchStatus: dispatch?.status,
        };
      });
      const status: BatchStatus = {
        batchId: payload.batch_id,
        total: payload.total,
        statusCounts: payload.status_counts ?? {},
        tasks,
      };
      readRetryMetadata(payload, tasks, status);
      return status;
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
      if (!isBatchId(sourceBatchId)) throw new Error(RETRY_REQUEST_ERROR);
      const key = requireIdempotencyKey(idempotencyKey);
      const bodySources = serializeRetrySources(sources);
      const payload = await request<unknown>(`/batch-commands/${sourceBatchId}/retry`, {
        method: "POST",
        token,
        stepUpProof,
        idempotencyKey: key,
        body: { sources: bodySources },
      });
      return parseBatchRetryResult(payload, sourceBatchId, sources);
    },
  };
}
