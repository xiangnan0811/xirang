import type {
  AssetRef,
  BackupExportArchiveFormat,
  BackupExportArchiveProfile,
  BackupExportAttemptProgress,
  BackupExportCleanupState,
  BackupExportCreateResult,
  BackupExportDownloadTicket,
  BackupExportErrorCategory,
  BackupExportExecutionState,
  BackupExportItemState,
  BackupExportItemStatus,
  BackupExportJob,
  BackupExportResultKind,
} from "@/types/domain";

import { request, type RequestOptions } from "./core";
import {
	mapBackupAssetEntryId,
	mapOpaqueBackupAssetId,
	mapSafeNonNegativeInteger,
} from "./backup-assets-boundary";

type BackupExportRequester = (path: string, options: RequestOptions) => Promise<unknown>;

const defaultRequester: BackupExportRequester = (path, options) => request<unknown>(path, options);
const OPAQUE_ID = /^[0-9a-f]{32}$/;
const DIGEST = /^[0-9a-f]{64}$/;
const CURSOR = /^[A-Za-z0-9_-]{1,4096}$/;
const CONTENT_URL = /^\/api\/v1\/asset-content\/[0-9a-f]{32}$/;
const ETAG = /^(?:W\/)?"[A-Za-z0-9._~:-]{1,128}"$/;
const RFC3339_INSTANT = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|([+-])(\d{2}):(\d{2}))$/;
const RFC3339_FRACTION = /\.(\d{1,9})(?=Z|[+-]\d{2}:\d{2}$)/;
const DEFAULT_ITEMS_LIMIT = 100;
const MAX_ITEMS_LIMIT = 200;

const EXECUTION_STATES = [
  "queued", "running", "retry_wait", "sealing", "ready", "cancel_requested",
  "failed", "source_expired", "canceled", "expiring", "expired",
] as const satisfies readonly BackupExportExecutionState[];
const CLEANUP_STATES = ["none", "revoking", "purging", "purged", "purge_failed"] as const satisfies readonly BackupExportCleanupState[];
const ITEM_STATES = ["pending", "read", "packed", "skipped", "failed"] as const satisfies readonly BackupExportItemState[];
const ATTEMPT_STATES = ["active", "sealing", "sealed", "failed", "canceled", "superseded"] as const;
const RESULT_KINDS = ["complete", "partial"] as const satisfies readonly BackupExportResultKind[];
const FORMATS = ["zip", "tar"] as const satisfies readonly BackupExportArchiveFormat[];
const PROFILES = ["zip_deflate_v1", "tar_none_v1", "tar_gzip_v1"] as const satisfies readonly BackupExportArchiveProfile[];
const ERROR_CATEGORIES = [
  "source_changed", "source_expired", "link_metadata_unavailable", "special_file_skipped",
  "artifact_missing", "artifact_tampered", "key_unavailable", "quota_exceeded", "deadline",
  "canceled", "internal_failure", "worker_unavailable", "provider_unavailable",
] as const satisfies readonly BackupExportErrorCategory[];
const CLEANUP_PRODUCTS: Readonly<Record<BackupExportExecutionState, readonly BackupExportCleanupState[]>> = {
  queued: ["none"],
  running: ["none"],
  retry_wait: ["none"],
  sealing: ["none"],
  ready: ["none"],
  cancel_requested: ["none", "revoking", "purging", "purged", "purge_failed"],
  failed: ["none", "revoking", "purging", "purged", "purge_failed"],
  source_expired: ["none", "revoking", "purging", "purged", "purge_failed"],
  expiring: ["none", "revoking", "purging", "purged", "purge_failed"],
  canceled: ["purged", "purge_failed"],
  expired: ["purged", "purge_failed"],
};

type RawObject = Record<string, unknown>;

function object(value: unknown, required: readonly string[], optional: readonly string[] = []): RawObject {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("invalid backup export response");
  }
  const raw = value as RawObject;
  const allowed = new Set([...required, ...optional]);
  if (required.some((key) => !(key in raw)) || Object.keys(raw).some((key) => !allowed.has(key))) {
    throw new Error("invalid backup export response");
  }
  return raw;
}

function closed<T extends string>(value: unknown, allowed: readonly T[]): T {
  if (typeof value !== "string" || !allowed.includes(value as T)) {
    throw new Error("invalid backup export response");
  }
  return value as T;
}

function validArchivePair(format: unknown, profile: unknown): boolean {
  return (format === "zip" && profile === "zip_deflate_v1") ||
    (format === "tar" && (profile === "tar_none_v1" || profile === "tar_gzip_v1"));
}

function validLifecycleProduct(
  executionState: BackupExportExecutionState,
  cleanupState: BackupExportCleanupState,
): boolean {
  return CLEANUP_PRODUCTS[executionState].includes(cleanupState);
}

function id(value: unknown): string {
  if (typeof value !== "string" || !OPAQUE_ID.test(value)) throw new Error("invalid backup export response");
  return value;
}

function digest(value: unknown): string {
  if (typeof value !== "string" || !DIGEST.test(value)) throw new Error("invalid backup export response");
  return value;
}

function safeInteger(value: unknown): number {
  const mapped = mapSafeNonNegativeInteger(value);
  if (mapped === null) throw new Error("invalid backup export response");
  return mapped;
}

function positiveInteger(value: unknown, maximum: number): number {
  const mapped = safeInteger(value);
  if (mapped < 1 || mapped > maximum) throw new Error("invalid backup export response");
  return mapped;
}

function validRFC3339Instant(value: string): boolean {
  const match = RFC3339_INSTANT.exec(value);
  if (!match) return false;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  const offsetHour = match[8] === undefined ? 0 : Number(match[8]);
  const offsetMinute = match[9] === undefined ? 0 : Number(match[9]);
  if (month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59 || offsetHour > 23 || offsetMinute > 59) {
    return false;
  }
  if (match[7] === "-" && offsetHour === 0 && offsetMinute === 0) return false;
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const monthDays = [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  return day >= 1 && day <= monthDays[month - 1];
}

interface WireRFC3339Instant {
  raw: string;
  display: string;
  epochNanoseconds: bigint;
}

function utc(value: unknown): WireRFC3339Instant;
function utc(value: unknown, optional: true): WireRFC3339Instant | null;
function utc(value: unknown, optional = false): WireRFC3339Instant | null {
	if (optional && (value === undefined || value === null)) return null;
	if (typeof value !== "string" || !validRFC3339Instant(value)) throw new Error("invalid backup export response");
	const epochNanoseconds = rfc3339EpochNanoseconds(value);
	return {
		raw: value,
		display: canonicalUTCInstant(epochNanoseconds),
		epochNanoseconds,
	};
}

function rfc3339EpochNanoseconds(value: string): bigint {
  const match = RFC3339_INSTANT.exec(value);
  if (!match) throw new Error("invalid backup export response");
  const fraction = RFC3339_FRACTION.exec(value)?.[1] ?? "";
  const calendar = new Date(0);
  calendar.setUTCFullYear(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
  calendar.setUTCHours(Number(match[4]), Number(match[5]), Number(match[6]), 0);
  const localMilliseconds = calendar.getTime();
  if (!Number.isSafeInteger(localMilliseconds)) throw new Error("invalid backup export response");
  const offsetMinutes = match[7] === undefined ? 0 : Number(match[8]) * 60 + Number(match[9]);
  const signedOffsetMinutes = match[7] === "+" ? offsetMinutes : -offsetMinutes;
  const nanoseconds = fraction === "" ? 0n : BigInt(fraction.padEnd(9, "0"));
	return (BigInt(localMilliseconds) - BigInt(signedOffsetMinutes) * 60_000n) * 1_000_000n + nanoseconds;
}

function canonicalUTCInstant(epochNanoseconds: bigint): string {
	const nanosecondsPerMillisecond = 1_000_000n;
	let milliseconds = epochNanoseconds / nanosecondsPerMillisecond;
	let subMillisecondNanoseconds = epochNanoseconds % nanosecondsPerMillisecond;
	if (subMillisecondNanoseconds < 0n) {
		milliseconds--;
		subMillisecondNanoseconds += nanosecondsPerMillisecond;
	}
	const epochMilliseconds = Number(milliseconds);
	if (!Number.isSafeInteger(epochMilliseconds)) throw new Error("invalid backup export response");
	const instant = new Date(epochMilliseconds);
	if (Number.isNaN(instant.getTime())) throw new Error("invalid backup export response");
	const year = instant.getUTCFullYear();
	if (year < 0 || year > 9999) throw new Error("invalid backup export response");
	const fractionalNanoseconds = BigInt(instant.getUTCMilliseconds()) * nanosecondsPerMillisecond + subMillisecondNanoseconds;
	const fraction = fractionalNanoseconds.toString().padStart(9, "0");
	const displayFraction = fraction.slice(0, 3) + fraction.slice(3).replace(/0+$/, "");
	return `${instant.toISOString().slice(0, 19)}.${displayFraction}Z`;
}

function compareEpochNanoseconds(leftNanoseconds: bigint, rightNanoseconds: bigint): number {
  if (leftNanoseconds < rightNanoseconds) return -1;
  if (leftNanoseconds > rightNanoseconds) return 1;
  return 0;
}

function compareRFC3339Instants(left: WireRFC3339Instant, right: WireRFC3339Instant): number {
  return compareEpochNanoseconds(left.epochNanoseconds, right.epochNanoseconds);
}

function browserNowEpochNanoseconds(): bigint {
  return BigInt(Date.now()) * 1_000_000n;
}

function optionalError(value: unknown): BackupExportErrorCategory | null {
  if (value === undefined || value === "") return null;
  return closed(value, ERROR_CATEGORIES);
}

function optionalCursor(value: unknown): string | null {
  if (value === undefined || value === "") return null;
  if (typeof value !== "string" || !CURSOR.test(value)) throw new Error("invalid backup export response");
  return value;
}

function booleanValue(value: unknown): boolean {
  if (typeof value !== "boolean") throw new Error("invalid backup export response");
  return value;
}

function stepUpProof(value: unknown): string {
  if (typeof value !== "string" || value.length === 0 || value.length > 8192 || /[\r\n\0\s]/.test(value)) {
    throw new Error("invalid backup export request");
  }
  return value;
}

function mapItem(value: unknown, itemCount: number): BackupExportItemStatus {
  const raw = object(value, ["id", "ordinal", "state", "logical_bytes", "provider_bytes"], ["error_category"]);
  const item = {
    id: id(raw.id),
    ordinal: safeInteger(raw.ordinal),
    state: closed(raw.state, ITEM_STATES),
    logicalBytes: safeInteger(raw.logical_bytes),
    providerBytes: safeInteger(raw.provider_bytes),
    errorCategory: optionalError(raw.error_category),
  } satisfies BackupExportItemStatus;
  if (item.ordinal >= itemCount ||
      ((item.state === "pending" || item.state === "read" || item.state === "packed") && item.errorCategory !== null) ||
      ((item.state === "skipped" || item.state === "failed") && item.errorCategory === null)) {
    throw new Error("invalid backup export response");
  }
  return item;
}

interface MappedBackupExportAttempt {
  attempt: BackupExportAttemptProgress;
  leaseExpiresAt: WireRFC3339Instant;
}

function mapAttempt(value: unknown): MappedBackupExportAttempt {
  const raw = object(value, ["attempt_number", "state", "item_count", "logical_bytes", "provider_bytes", "lease_expires_at"]);
  const leaseExpiresAt = utc(raw.lease_expires_at);
  return {
    attempt: {
      attemptNumber: positiveInteger(raw.attempt_number, 1_000_000),
      state: closed(raw.state, ATTEMPT_STATES),
      itemCount: safeInteger(raw.item_count),
      logicalBytes: safeInteger(raw.logical_bytes),
      providerBytes: safeInteger(raw.provider_bytes),
      leaseExpiresAt: leaseExpiresAt.display,
    },
    leaseExpiresAt,
  };
}

function attemptMatchesExecution(
  executionState: BackupExportExecutionState,
  attempt: BackupExportAttemptProgress | null,
): boolean {
  if (attempt === null) return executionState !== "running" && executionState !== "sealing";
  if (executionState === "running") return attempt.state === "active";
  if (executionState === "sealing") return attempt.state === "sealing";
  if (executionState === "cancel_requested") {
    return attempt.state === "active" || attempt.state === "sealing" || attempt.state === "sealed";
  }
  if (executionState === "ready" || executionState === "expiring" || executionState === "expired") {
    return attempt.state === "sealed";
  }
  return false;
}

function hasContiguousAscendingOrdinals(items: readonly BackupExportItemStatus[]): boolean {
  let nextOrdinal: number | null = null;
  for (const item of items) {
    if (nextOrdinal !== null && item.ordinal !== nextOrdinal) return false;
    nextOrdinal = item.ordinal + 1;
  }
  return true;
}

export function mapBackupExportJob(value: unknown, itemsLimit = DEFAULT_ITEMS_LIMIT): BackupExportJob {
  if (!Number.isSafeInteger(itemsLimit) || itemsLimit < 1 || itemsLimit > MAX_ITEMS_LIMIT) {
    throw new Error("invalid backup export response");
  }
  const raw = object(
    value,
    [
      "schema_version", "id", "selection_digest", "archive_format", "archive_profile", "execution_state",
      "cleanup_state", "item_count", "packed_count", "skipped_count", "failed_count", "logical_bytes",
      "provider_bytes", "artifact_bytes", "created_at", "absolute_deadline", "items", "poll_after_seconds", "can_cancel",
      "can_download",
    ],
    ["result_kind", "error_category", "ready_at", "expires_at", "attempt", "next_cursor"],
  );
  if (raw.schema_version !== 1 || !Array.isArray(raw.items) || raw.items.length > itemsLimit) {
    throw new Error("invalid backup export response");
  }
  const itemCount = safeInteger(raw.item_count);
  const packedCount = safeInteger(raw.packed_count);
  const skippedCount = safeInteger(raw.skipped_count);
  const failedCount = safeInteger(raw.failed_count);
  const logicalBytes = safeInteger(raw.logical_bytes);
  const providerBytes = safeInteger(raw.provider_bytes);
  const artifactBytes = safeInteger(raw.artifact_bytes);
  const executionState = closed(raw.execution_state, EXECUTION_STATES);
  const cleanupState = closed(raw.cleanup_state, CLEANUP_STATES);
  const resultKind = raw.result_kind === undefined || raw.result_kind === ""
    ? null
    : closed(raw.result_kind, RESULT_KINDS);
  const createdAt = utc(raw.created_at);
  const absoluteDeadline = utc(raw.absolute_deadline);
  const readyAt = utc(raw.ready_at, true);
  const expiresAt = utc(raw.expires_at, true);
  const mappedAttempt = raw.attempt === undefined || raw.attempt === null ? null : mapAttempt(raw.attempt);
  const attempt = mappedAttempt === null ? null : mappedAttempt.attempt;
  const items = raw.items.map((item) => mapItem(item, itemCount));
  const nextCursor = optionalCursor(raw.next_cursor);
  const ordinals = new Set(items.map((item) => item.ordinal));
  const ids = new Set(items.map((item) => item.id));
  const terminalItemCount = packedCount + skippedCount + failedCount;
  if (itemCount === 0 || ordinals.size !== items.length || ids.size !== items.length ||
      !hasContiguousAscendingOrdinals(items) ||
      (nextCursor !== null && items.length !== itemsLimit) ||
      terminalItemCount > itemCount ||
      !validArchivePair(raw.archive_format, raw.archive_profile) ||
      (resultKind === "complete" && (packedCount !== itemCount || skippedCount !== 0 || failedCount !== 0)) ||
      (resultKind === "partial" && (packedCount === 0 || skippedCount + failedCount === 0)) ||
      (resultKind !== null && terminalItemCount !== itemCount) ||
      !validLifecycleProduct(executionState, cleanupState) ||
      compareRFC3339Instants(createdAt, absoluteDeadline) > 0 ||
      (readyAt !== null && compareRFC3339Instants(createdAt, readyAt) > 0) ||
      (readyAt !== null && expiresAt === null) ||
      (readyAt === null && expiresAt !== null) ||
      (readyAt !== null && expiresAt !== null && compareRFC3339Instants(readyAt, expiresAt) >= 0) ||
      (mappedAttempt !== null && (compareRFC3339Instants(mappedAttempt.leaseExpiresAt, absoluteDeadline) > 0 ||
        mappedAttempt.attempt.itemCount !== terminalItemCount || mappedAttempt.attempt.logicalBytes !== logicalBytes ||
        mappedAttempt.attempt.providerBytes !== providerBytes)) ||
      !attemptMatchesExecution(executionState, attempt)) {
    throw new Error("invalid backup export response");
  }
  const artifactState = executionState === "ready" || executionState === "expiring" || executionState === "expired";
  if (artifactState && (readyAt === null || expiresAt === null || resultKind === null || packedCount === 0 || artifactBytes === 0)) {
    throw new Error("invalid backup export response");
  }
  const forbidsArtifactProduct = executionState === "queued" || executionState === "running" ||
    executionState === "failed" || executionState === "source_expired" || executionState === "canceled";
  if (forbidsArtifactProduct &&
      (resultKind !== null || readyAt !== null || expiresAt !== null || artifactBytes !== 0)) {
    throw new Error("invalid backup export response");
  }
  const pollAfterSeconds = safeInteger(raw.poll_after_seconds);
  const shouldPoll = executionState === "queued" || executionState === "running" || executionState === "retry_wait" ||
    executionState === "sealing" || executionState === "cancel_requested" || executionState === "expiring";
  if (pollAfterSeconds > 300 || (shouldPoll ? pollAfterSeconds === 0 : pollAfterSeconds !== 0)) {
    throw new Error("invalid backup export response");
  }
  const canCancel = booleanValue(raw.can_cancel);
  const canDownload = booleanValue(raw.can_download);
  const cancelable = executionState === "queued" || executionState === "running" || executionState === "retry_wait" ||
    executionState === "sealing" || executionState === "ready" || executionState === "cancel_requested";
  const downloadStateAllowsPermission = executionState === "ready" && cleanupState === "none" && expiresAt !== null;
  if (canCancel !== cancelable ||
      (canDownload && !downloadStateAllowsPermission)) {
    throw new Error("invalid backup export response");
  }
  const result: BackupExportJob = {
    schemaVersion: 1,
    id: id(raw.id),
    selectionDigest: digest(raw.selection_digest),
    archiveFormat: closed(raw.archive_format, FORMATS),
    archiveProfile: closed(raw.archive_profile, PROFILES),
    executionState,
    resultKind,
    cleanupState,
    itemCount,
    packedCount,
    skippedCount,
    failedCount,
    logicalBytes,
    providerBytes,
    artifactBytes,
    errorCategory: optionalError(raw.error_category),
    createdAt: createdAt.display,
    absoluteDeadline: absoluteDeadline.display,
    readyAt: readyAt?.display ?? null,
    expiresAt: expiresAt?.display ?? null,
    attempt,
    items,
    nextCursor,
    pollAfterSeconds,
    canCancel,
    canDownload,
  };
  if ((executionState === "failed" || executionState === "source_expired") && result.errorCategory === null) {
    throw new Error("invalid backup export response");
  }
  return result;
}

function validAssetRef(ref: AssetRef): boolean {
  return mapOpaqueBackupAssetId(ref.recoveryPointId) !== null && mapBackupAssetEntryId(ref.entryId) !== null;
}

export type BackupExportCreateInput =
  | {
      selection: { schemaVersion: 1; kind: "explicit"; refs: AssetRef[] };
      archiveFormat: BackupExportArchiveFormat;
      archiveProfile: BackupExportArchiveProfile;
      idempotencyKey: string;
    }
  | {
      selection: { schemaVersion: 1; kind: "saved_search"; savedSearchId: string; savedSearchVersion: number };
      archiveFormat: BackupExportArchiveFormat;
      archiveProfile: BackupExportArchiveProfile;
      idempotencyKey: string;
    };

function encodeSelection(input: BackupExportCreateInput["selection"]): RawObject {
  if (input.schemaVersion !== 1) throw new Error("invalid backup export request");
  if (input.kind === "explicit") {
    if (input.refs.length === 0 || input.refs.some((ref) => !validAssetRef(ref))) throw new Error("invalid backup export request");
    const keys = new Set(input.refs.map((ref) => `${ref.recoveryPointId}:${ref.entryId}`));
    if (keys.size !== input.refs.length) throw new Error("invalid backup export request");
    return {
      schema_version: 1,
      kind: "explicit",
      refs: input.refs.map((ref) => ({ recovery_point_id: ref.recoveryPointId, entry_id: ref.entryId })),
    };
  }
  if (input.kind !== "saved_search") throw new Error("invalid backup export request");
  if (!OPAQUE_ID.test(input.savedSearchId) || !Number.isSafeInteger(input.savedSearchVersion) || input.savedSearchVersion < 1) {
    throw new Error("invalid backup export request");
  }
  return {
    schema_version: 1,
    kind: "saved_search",
    saved_search_id: input.savedSearchId,
    saved_search_version: input.savedSearchVersion,
  };
}

function validateCreateInput(input: BackupExportCreateInput): void {
  if (!validArchivePair(input.archiveFormat, input.archiveProfile)) throw new Error("invalid backup export request");
  if (typeof input.idempotencyKey !== "string" || input.idempotencyKey.trim() !== input.idempotencyKey || input.idempotencyKey.length < 16 || input.idempotencyKey.length > 256) {
    throw new Error("invalid backup export request");
  }
  encodeSelection(input.selection);
}

function mapCreateResult(value: unknown): BackupExportCreateResult {
  const raw = object(value, ["job", "replay"]);
  return { job: mapBackupExportJob(raw.job), replay: booleanValue(raw.replay) };
}

function mapTicket(value: unknown): BackupExportDownloadTicket {
  const raw = object(value, ["schema_version", "content_url", "content_type", "content_length", "etag", "range", "expires_at", "idle_expires_at"]);
  if (raw.schema_version !== 1 || typeof raw.content_url !== "string" || !CONTENT_URL.test(raw.content_url) ||
      typeof raw.content_type !== "string" || !["application/zip", "application/x-tar", "application/gzip"].includes(raw.content_type) ||
      raw.range !== "single" || typeof raw.etag !== "string" || !ETAG.test(raw.etag)) throw new Error("invalid backup export ticket");
  const expiresAt = utc(raw.expires_at);
  const idleExpiresAt = utc(raw.idle_expires_at);
  const now = browserNowEpochNanoseconds();
  if (compareEpochNanoseconds(expiresAt.epochNanoseconds, now) <= 0 ||
      compareEpochNanoseconds(idleExpiresAt.epochNanoseconds, now) <= 0 ||
      compareRFC3339Instants(idleExpiresAt, expiresAt) > 0) {
    throw new Error("invalid backup export ticket");
  }
  return {
    schemaVersion: 1,
    contentUrl: raw.content_url,
    contentType: raw.content_type,
    contentLength: safeInteger(raw.content_length),
    etag: raw.etag,
    range: "single",
    expiresAt: expiresAt.display,
    idleExpiresAt: idleExpiresAt.display,
  };
}

export function createBackupExportsApi(requester: BackupExportRequester = defaultRequester) {
  return {
    async create(
      token: string,
      input: BackupExportCreateInput,
      freshStepUpProof: string,
      signal?: AbortSignal,
    ): Promise<BackupExportCreateResult> {
      validateCreateInput(input);
      const raw = await requester("/asset-exports", {
        method: "POST",
        token,
        stepUpProof: stepUpProof(freshStepUpProof),
        idempotencyKey: input.idempotencyKey,
        signal,
        body: {
          schema_version: 1,
          selection: encodeSelection(input.selection),
          archive_format: input.archiveFormat,
          archive_profile: input.archiveProfile,
        },
      });
      return mapCreateResult(raw);
    },
    async status(token: string, exportJobId: string, options: { cursor?: string; limit?: number; signal?: AbortSignal } = {}): Promise<BackupExportJob> {
      if (!OPAQUE_ID.test(exportJobId)) throw new Error("invalid backup export request");
      const limit = options.limit ?? DEFAULT_ITEMS_LIMIT;
      if (!Number.isSafeInteger(limit) || limit < 1 || limit > MAX_ITEMS_LIMIT) throw new Error("invalid backup export request");
      if (options.cursor !== undefined && !CURSOR.test(options.cursor)) throw new Error("invalid backup export request");
      const query = new URLSearchParams({ items_limit: String(limit) });
      if (options.cursor) query.set("items_cursor", options.cursor);
      const raw = await requester(`/asset-exports/${exportJobId}?${query.toString()}`, {
        method: "GET", token, signal: options.signal,
      });
      const job = mapBackupExportJob(raw, limit);
      if (job.id !== exportJobId) throw new Error("invalid backup export response");
      return job;
    },
    async cancel(token: string, exportJobId: string, signal?: AbortSignal): Promise<BackupExportJob> {
      if (!OPAQUE_ID.test(exportJobId)) throw new Error("invalid backup export request");
      const raw = await requester(`/asset-exports/${exportJobId}/cancel`, {
        method: "POST", token, signal, body: { schema_version: 1 },
      });
      const job = mapBackupExportJob(raw);
      if (job.id !== exportJobId) throw new Error("invalid backup export response");
      return job;
    },
    async issueDownloadTicket(token: string, exportJobId: string, freshStepUpProof: string, signal?: AbortSignal): Promise<BackupExportDownloadTicket> {
      if (!OPAQUE_ID.test(exportJobId)) throw new Error("invalid backup export request");
      const raw = await requester(`/asset-exports/${exportJobId}/download-ticket`, {
        method: "POST", token, stepUpProof: stepUpProof(freshStepUpProof), signal, body: { schema_version: 1 },
      });
      return mapTicket(raw);
    },
  };
}

export type BackupExportsApi = ReturnType<typeof createBackupExportsApi>;
