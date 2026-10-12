import i18n from "@/i18n";
import type {
  BatchRetryEligibility,
  BatchRetryFailureKind,
  BatchRetryLink,
  BatchRetryReason,
  BatchRetryResult,
  BatchRetrySource,
  BatchStatus,
} from "./batch-api";

interface BatchDispatchResponse {
  task_id: number;
  status: "pending" | "dispatching" | "accepted" | "failed";
  last_error?: string;
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

const RETRY_REASON: Record<BatchRetryReason, true> = {
  eligible: true,
  legacy: true,
  source_changed: true,
  source_missing: true,
  not_failed: true,
  active: true,
  effects_pending: true,
  history_unverified: true,
  already_retried: true,
};

interface RetrySourceWire {
  task_id: unknown;
  node_id: unknown;
  run_id: unknown;
  eligible: unknown;
  reason: unknown;
  failure_kind: unknown;
  retry_batch_id: unknown;
}

interface RetryLinkWire {
  task_id: unknown;
  run_id: unknown;
  batch_id: unknown;
  deleted: unknown;
}

interface RetryResultWire {
  batch_id: unknown;
  source_batch_id: unknown;
  retain: unknown;
  task_ids: unknown;
  run_ids: unknown;
}

export class BatchRetryContractError extends Error {
  constructor() {
    super("invalid batch retry response");
    this.name = "BatchRetryContractError";
  }
}

// crypto/rand.Text is at least 26 base32 characters; the batch id column stops at 64.
const BATCH_ID_PATTERN = /^[A-Z2-7]{26,64}$/;

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
  return typeof value === "string" && Object.hasOwn(RETRY_REASON, value);
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

function parseRetrySource(
  row: unknown,
  nodes: Map<number, number>,
  retain: boolean | undefined,
): BatchRetryEligibility | null {
  if (typeof row !== "object" || row === null || Array.isArray(row) || !Object.hasOwn(row, "failure_kind") || !Object.hasOwn(row, "retry_batch_id")) {
    return null;
  }
  const source = row as RetrySourceWire;
  const taskId = source.task_id;
  const nodeId = source.node_id;
  const runId = source.run_id;
  const eligible = source.eligible;
  const reason = source.reason;
  if (!isPositiveSafeInteger(taskId) || !isPositiveSafeInteger(nodeId) || !isNonNegativeSafeInteger(runId)) {
    return null;
  }
  if (nodes.get(taskId) !== nodeId || typeof eligible !== "boolean" || !isRetryReason(reason)) {
    return null;
  }

  let failureKind: BatchRetryFailureKind | null;
  if (source.failure_kind === null) failureKind = null;
  else if (source.failure_kind === "dispatch" || source.failure_kind === "execution") failureKind = source.failure_kind;
  else return null;

  let retryBatchId: string | null;
  if (source.retry_batch_id === null) retryBatchId = null;
  else if (isBatchId(source.retry_batch_id)) retryBatchId = source.retry_batch_id;
  else return null;

  if (!failureShapeAllowed(failureKind, runId, eligible, reason, retryBatchId)) return null;
  const operable = (failureKind === "dispatch" && runId === 0) || (failureKind === "execution" && runId > 0);
  if (reason === "already_retried") {
    if (eligible || retryBatchId === null || !operable) return null;
  } else if (retryBatchId !== null) {
    return null;
  } else if (reason === "eligible") {
    if (!eligible || retain === undefined || !operable) return null;
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
  if (typeof row !== "object" || row === null || Array.isArray(row)) return null;
  const link = row as RetryLinkWire;
  const taskId = link.task_id;
  const runId = link.run_id;
  const batchId = link.batch_id;
  const deleted = link.deleted;
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

function parseSafeIdList(value: unknown, minimum: number): number[] | null {
  if (!Array.isArray(value) || value.length === 0) return null;
  const ids: number[] = [];
  for (const item of value) {
    if (typeof item !== "number" || !Number.isSafeInteger(item) || item < minimum) return null;
    ids.push(item);
  }
  return ids;
}

export function mapBatchStatus(payload: unknown): BatchStatus {
  const body = payload as BatchStatusResponse;
  const dispatches = new Map((body.dispatches ?? []).map((dispatch) => [dispatch.task_id, dispatch]));
  const tasks = (body.tasks ?? []).map((task) => {
    const dispatch = dispatches.get(task.id);
    const dispatchError = dispatch && dispatch.status !== "accepted" ? dispatch.last_error : undefined;
    return {
      id: task.id,
      name: task.name,
      status: task.status,
      nodeId: task.node?.id ?? task.node_id,
      nodeName: task.node?.name ?? i18n.t("common.nodeDefault", { id: task.node_id }),
      lastError: task.last_error || dispatchError,
      dispatchStatus: dispatch?.status,
    };
  });
  const status: BatchStatus = {
    batchId: body.batch_id,
    total: body.total,
    statusCounts: body.status_counts ?? {},
    tasks,
  };
  readRetryMetadata(body, tasks, status);
  return status;
}

export function parseBatchRetryResult(
  payload: unknown,
  sourceBatchId: string,
  sources: readonly BatchRetrySource[],
): BatchRetryResult {
  if (typeof payload !== "object" || payload === null || Array.isArray(payload)) {
    throw new BatchRetryContractError();
  }
  const body = payload as RetryResultWire;
  if (!isBatchId(body.batch_id) || body.batch_id === sourceBatchId) {
    throw new BatchRetryContractError();
  }
  if (body.source_batch_id !== sourceBatchId || typeof body.retain !== "boolean") {
    throw new BatchRetryContractError();
  }
  const taskIds = parseSafeIdList(body.task_ids, 1);
  const runIds = parseSafeIdList(body.run_ids, 0);
  if (taskIds === null || runIds === null || taskIds.length !== runIds.length) {
    throw new BatchRetryContractError();
  }
  if (new Set(taskIds).size !== taskIds.length) throw new BatchRetryContractError();
  const sourceTaskIds = new Set(sources.map((source) => source.taskId));
  if (taskIds.some((taskId) => sourceTaskIds.has(taskId))) throw new BatchRetryContractError();
  return {
    batchId: body.batch_id,
    sourceBatchId,
    taskIds,
    runIds,
    retain: body.retain,
  };
}

export interface BatchCommandResponseModule {
  mapBatchStatus: (payload: unknown) => BatchStatus;
  parseBatchRetryResult: (
    payload: unknown,
    sourceBatchId: string,
    sources: readonly BatchRetrySource[],
  ) => BatchRetryResult;
}
