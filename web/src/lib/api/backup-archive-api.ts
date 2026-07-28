import type {
  AssetRef,
  BackupArchiveEntryWarning,
  BackupArchiveFailureProduct,
  BackupArchiveFallback,
  BackupArchiveIndex,
  BackupArchiveIndexEntry,
  BackupArchiveMemberCreateResult,
  BackupArchiveMemberState,
  BackupArchiveMemberStatus,
  BackupExportDownloadTicket,
} from "@/types/domain";

import { request, type RequestOptions } from "./core";
import {
  mapBackupAssetEntryId,
  mapOpaqueBackupAssetId,
  mapSafeNonNegativeInteger,
} from "./backup-assets-boundary";

type BackupArchiveRequester = (path: string, options: RequestOptions) => Promise<unknown>;

const defaultRequester: BackupArchiveRequester = (path, options) => request<unknown>(path, options);
const OPAQUE_ID = /^[0-9a-f]{32}$/;
const DIGEST = /^[0-9a-f]{64}$/;
const CONTENT_URL = /^\/api\/v1\/asset-content\/[0-9a-f]{32}$/;
const ETAG = /^(?:W\/)?"[A-Za-z0-9._~:-]{1,128}"$/;
const RFC3339_INSTANT = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|([+-])(\d{2}):(\d{2}))$/;
const RFC3339_FRACTION = /\.(\d{1,9})(?=Z|[+-]\d{2}:\d{2}$)/;
const MEMBER_STATES = ["queued", "running", "ready", "failed", "canceled", "expired"] as const satisfies readonly BackupArchiveMemberState[];
const FAILURE_PRODUCTS = ["encrypted", "unsupported", "limit", "unsafe", "unavailable"] as const satisfies readonly BackupArchiveFailureProduct[];
const ENTRY_WARNINGS = ["none"] as const satisfies readonly BackupArchiveEntryWarning[];
const MAX_ARCHIVE_INDEX_ENTRIES = 100_000;

type RawObject = Record<string, unknown>;

function object(value: unknown, required: readonly string[], optional: readonly string[] = []): RawObject {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("invalid backup archive response");
  }
  const raw = value as RawObject;
  const allowed = new Set([...required, ...optional]);
  if (required.some((key) => !(key in raw)) || Object.keys(raw).some((key) => !allowed.has(key))) {
    throw new Error("invalid backup archive response");
  }
  return raw;
}

function closed<T extends string>(value: unknown, allowed: readonly T[]): T {
  if (typeof value !== "string" || !allowed.includes(value as T)) throw new Error("invalid backup archive response");
  return value as T;
}

function id(value: unknown): string {
  if (typeof value !== "string" || !OPAQUE_ID.test(value)) throw new Error("invalid backup archive response");
  return value;
}

function digest(value: unknown): string {
  if (typeof value !== "string" || !DIGEST.test(value)) throw new Error("invalid backup archive response");
  return value;
}

function assetRef(value: unknown): AssetRef {
  const raw = object(value, ["recovery_point_id", "entry_id"]);
  const recoveryPointId = typeof raw.recovery_point_id === "string"
    ? mapOpaqueBackupAssetId(raw.recovery_point_id)
    : null;
  const entryId = typeof raw.entry_id === "string" ? mapBackupAssetEntryId(raw.entry_id) : null;
  if (recoveryPointId === null || entryId === null) throw new Error("invalid backup archive response");
  return { recoveryPointId, entryId };
}

function sameAssetRef(left: AssetRef, right: AssetRef): boolean {
  return left.recoveryPointId === right.recoveryPointId && left.entryId === right.entryId;
}

function safeInteger(value: unknown): number {
  const result = mapSafeNonNegativeInteger(value);
  if (result === null) throw new Error("invalid backup archive response");
  return result;
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
  display: string;
  epochNanoseconds: bigint;
}

function utc(value: unknown): WireRFC3339Instant {
  if (typeof value !== "string" || !validRFC3339Instant(value)) throw new Error("invalid backup archive response");
  const epochNanoseconds = rfc3339EpochNanoseconds(value);
  return {
    display: canonicalUTCInstant(epochNanoseconds),
    epochNanoseconds,
  };
}

function rfc3339EpochNanoseconds(value: string): bigint {
  const match = RFC3339_INSTANT.exec(value);
  if (!match) throw new Error("invalid backup archive response");
  const fraction = RFC3339_FRACTION.exec(value)?.[1] ?? "";
  const calendar = new Date(0);
  calendar.setUTCFullYear(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
  calendar.setUTCHours(Number(match[4]), Number(match[5]), Number(match[6]), 0);
  const localMilliseconds = calendar.getTime();
  if (!Number.isSafeInteger(localMilliseconds)) throw new Error("invalid backup archive response");
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
  if (!Number.isSafeInteger(epochMilliseconds)) throw new Error("invalid backup archive response");
  const instant = new Date(epochMilliseconds);
  if (Number.isNaN(instant.getTime())) throw new Error("invalid backup archive response");
  const year = instant.getUTCFullYear();
  if (year < 0 || year > 9999) throw new Error("invalid backup archive response");
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

function browserNowEpochNanoseconds(): bigint {
  return BigInt(Date.now()) * 1_000_000n;
}

function booleanValue(value: unknown): boolean {
  if (typeof value !== "boolean") throw new Error("invalid backup archive response");
  return value;
}

function stepUpProof(value: unknown): string {
  if (typeof value !== "string" || value.length === 0 || value.length > 8192 || /[\r\n\0\s]/.test(value)) {
    throw new Error("invalid backup archive request");
  }
  return value;
}

function safeDisplay(value: unknown, maximum: number): string {
  if (typeof value !== "string" || value.trim() !== value || value.length === 0 || value.length > maximum || /[\0\r\n]/.test(value)) {
    throw new Error("invalid backup archive response");
  }
  return value;
}

function safeMemberDisplayName(value: unknown): string {
  const displayName = safeDisplay(value, 512);
  if (displayName === "." || displayName === ".." || /[/\\]/.test(displayName)) {
    throw new Error("invalid backup archive response");
  }
  return displayName;
}

function mapIndexEntry(value: unknown): BackupArchiveIndexEntry {
  const raw = object(value, ["id", "display_name", "type", "size", "media_type", "warning"], ["parent_id"]);
  if (raw.type !== "file") throw new Error("invalid backup archive response");
  const parentId = raw.parent_id === undefined || raw.parent_id === null ? null : id(raw.parent_id);
  return {
    id: id(raw.id),
    parentId,
    displayName: safeMemberDisplayName(raw.display_name),
    type: "file",
    size: safeInteger(raw.size),
    mediaType: safeDisplay(raw.media_type, 128),
    warning: closed(raw.warning, ENTRY_WARNINGS),
  };
}

function validateHierarchy(entries: BackupArchiveIndexEntry[]): void {
  const byId = new Map<string, BackupArchiveIndexEntry>();
  const displayKeys = new Set<string>();
  for (const entry of entries) {
    if (byId.has(entry.id) || entry.parentId === entry.id) {
      throw new Error("invalid backup archive response");
    }
    const displayKey = `${entry.parentId ?? ""}\0${entry.displayName.normalize("NFKC").toLocaleLowerCase("en-US")}`;
    if (displayKeys.has(displayKey)) throw new Error("invalid backup archive response");
    byId.set(entry.id, entry);
    displayKeys.add(displayKey);
  }

  const visitState = new Map<string, 1 | 2>();
  for (const entry of entries) {
    if (visitState.get(entry.id) === 2) continue;
    const path: string[] = [];
    let current: string | null = entry.id;
    while (current !== null) {
      const state = visitState.get(current);
      if (state === 1) throw new Error("invalid backup archive response");
      if (state === 2) break;
      const currentEntry = byId.get(current);
      if (!currentEntry) break;
      visitState.set(current, 1);
      path.push(current);
      current = currentEntry.parentId;
    }
    for (const id of path) {
      visitState.set(id, 2);
    }
  }
}

export function mapBackupArchiveIndex(value: unknown): BackupArchiveIndex {
  const raw = object(value, ["schema_version", "index_revision", "expires_at", "entries"]);
  if (raw.schema_version !== 1 || !Array.isArray(raw.entries) || raw.entries.length > MAX_ARCHIVE_INDEX_ENTRIES) {
    throw new Error("invalid backup archive response");
  }
  const expiresAt = utc(raw.expires_at);
  if (compareEpochNanoseconds(expiresAt.epochNanoseconds, browserNowEpochNanoseconds()) <= 0) {
    throw new Error("invalid backup archive response");
  }
  const entries = raw.entries.map(mapIndexEntry);
  validateHierarchy(entries);
  return {
    schemaVersion: 1,
    indexRevision: digest(raw.index_revision),
    expiresAt: expiresAt.display,
    entries,
  };
}

function mapFallback(value: unknown, failureProduct: BackupArchiveFailureProduct | null): BackupArchiveFallback {
  const raw = object(value, [], ["action", "reason"]);
  const action = raw.action === undefined || raw.action === "" ? null : closed(raw.action, ["download_original"] as const);
  const reason = raw.reason === undefined || raw.reason === "" ? null : closed(raw.reason, ["original_download_unavailable"] as const);
  const supportsOriginal = failureProduct === "encrypted" || failureProduct === "unsupported" || failureProduct === "limit";
  if ((action !== null && reason !== null) || (!supportsOriginal && (action !== null || reason !== null)) ||
      (supportsOriginal && action === null && reason === null)) {
    throw new Error("invalid backup archive response");
  }
  return { action, reason };
}

interface BoundArchiveStatus {
  status: BackupArchiveMemberStatus;
  assetRef: AssetRef;
  indexRevision: string;
}

function mapBoundArchiveStatus(value: unknown): BoundArchiveStatus {
  const raw = object(
    value,
    ["schema_version", "request_id", "asset_ref", "index_revision", "state", "fallback", "retryable", "terminal"],
    ["failure_product"],
  );
  if (raw.schema_version !== 1) throw new Error("invalid backup archive response");
  const state = closed(raw.state, MEMBER_STATES);
  const failureProduct = raw.failure_product === undefined || raw.failure_product === ""
    ? null
    : closed(raw.failure_product, FAILURE_PRODUCTS);
  const retryable = booleanValue(raw.retryable);
  const terminal = booleanValue(raw.terminal);
  const expectedTerminal = state === "ready" || state === "failed" || state === "canceled" || state === "expired";
  if (terminal !== expectedTerminal || retryable || (state === "failed") !== (failureProduct !== null)) {
    throw new Error("invalid backup archive response");
  }
  return {
    assetRef: assetRef(raw.asset_ref),
    indexRevision: digest(raw.index_revision),
    status: {
      schemaVersion: 1,
      requestId: id(raw.request_id),
      state,
      failureProduct,
      fallback: mapFallback(raw.fallback, failureProduct),
      retryable,
      terminal,
    },
  };
}

export function mapBackupArchiveStatus(value: unknown): BackupArchiveMemberStatus {
  return mapBoundArchiveStatus(value).status;
}

interface BoundArchiveCreateResult {
  result: BackupArchiveMemberCreateResult;
  assetRef: AssetRef;
  indexRevision: string;
}

function mapCreateResult(value: unknown): BoundArchiveCreateResult {
  const raw = object(value, ["schema_version", "request_id", "asset_ref", "index_revision", "state"]);
  if (raw.schema_version !== 1 || raw.state !== "queued") throw new Error("invalid backup archive response");
  return {
    assetRef: assetRef(raw.asset_ref),
    indexRevision: digest(raw.index_revision),
    result: { schemaVersion: 1, requestId: id(raw.request_id), state: "queued" },
  };
}

function mapTicket(value: unknown): BackupExportDownloadTicket {
  const raw = object(value, ["schema_version", "content_url", "content_type", "content_length", "etag", "range", "expires_at", "idle_expires_at"]);
  if (raw.schema_version !== 1 || typeof raw.content_url !== "string" || !CONTENT_URL.test(raw.content_url) ||
      typeof raw.content_type !== "string" || raw.content_type.trim() !== raw.content_type || raw.content_type.length === 0 ||
      raw.content_type.length > 128 || raw.range !== "none" || typeof raw.etag !== "string" || !ETAG.test(raw.etag)) {
    throw new Error("invalid backup archive response");
  }
  const expiresAt = utc(raw.expires_at);
  const idleExpiresAt = utc(raw.idle_expires_at);
  const now = browserNowEpochNanoseconds();
  if (compareEpochNanoseconds(expiresAt.epochNanoseconds, now) <= 0 ||
      compareEpochNanoseconds(idleExpiresAt.epochNanoseconds, now) <= 0 ||
      compareEpochNanoseconds(idleExpiresAt.epochNanoseconds, expiresAt.epochNanoseconds) > 0) {
    throw new Error("invalid backup archive response");
  }
  return {
    schemaVersion: 1,
    contentUrl: raw.content_url,
    contentType: raw.content_type,
    contentLength: safeInteger(raw.content_length),
    etag: raw.etag,
    range: "none",
    expiresAt: expiresAt.display,
    idleExpiresAt: idleExpiresAt.display,
  };
}

function validRef(ref: AssetRef): boolean {
  return mapOpaqueBackupAssetId(ref.recoveryPointId) !== null && mapBackupAssetEntryId(ref.entryId) !== null;
}

function basePath(ref: AssetRef): string {
  if (!validRef(ref)) throw new Error("invalid backup archive request");
  return `/recovery-points/${ref.recoveryPointId}/entries/${ref.entryId}`;
}

function validIdempotencyKey(value: string): boolean {
  return value.trim() === value && value.length >= 16 && value.length <= 256;
}

export function createBackupArchiveApi(requester: BackupArchiveRequester = defaultRequester) {
  return {
    async listIndex(token: string, ref: AssetRef, signal?: AbortSignal): Promise<BackupArchiveIndex> {
      const raw = await requester(`${basePath(ref)}/archive-members`, { method: "GET", token, signal });
      return mapBackupArchiveIndex(raw);
    },
    async create(token: string, ref: AssetRef, indexRevision: string, memberId: string, idempotencyKey: string, signal?: AbortSignal): Promise<BackupArchiveMemberCreateResult> {
      if (!DIGEST.test(indexRevision) || !OPAQUE_ID.test(memberId) || !validIdempotencyKey(idempotencyKey)) {
        throw new Error("invalid backup archive request");
      }
      const raw = await requester(`${basePath(ref)}/archive-member-jobs`, {
        method: "POST", token, idempotencyKey, signal,
        body: { schema_version: 1, index_revision: indexRevision, member_chain: [memberId] },
      });
      const bound = mapCreateResult(raw);
      if (!sameAssetRef(bound.assetRef, ref) || bound.indexRevision !== indexRevision) {
        throw new Error("invalid backup archive response");
      }
      return bound.result;
    },
    async status(token: string, ref: AssetRef, indexRevision: string, requestId: string, signal?: AbortSignal): Promise<BackupArchiveMemberStatus> {
      if (!DIGEST.test(indexRevision) || !OPAQUE_ID.test(requestId)) throw new Error("invalid backup archive request");
      const raw = await requester(
        `${basePath(ref)}/archive-member-jobs/${requestId}?index_revision=${encodeURIComponent(indexRevision)}`,
        { method: "GET", token, signal },
      );
      const bound = mapBoundArchiveStatus(raw);
      if (bound.status.requestId !== requestId || !sameAssetRef(bound.assetRef, ref) || bound.indexRevision !== indexRevision) {
        throw new Error("invalid backup archive response");
      }
      return bound.status;
    },
    async cancel(token: string, ref: AssetRef, indexRevision: string, requestId: string, signal?: AbortSignal): Promise<BackupArchiveMemberStatus> {
      if (!DIGEST.test(indexRevision) || !OPAQUE_ID.test(requestId)) throw new Error("invalid backup archive request");
      const raw = await requester(`${basePath(ref)}/archive-member-jobs/${requestId}/cancel`, {
        method: "POST", token, signal, body: { schema_version: 1, index_revision: indexRevision },
      });
      const bound = mapBoundArchiveStatus(raw);
      if (bound.status.requestId !== requestId || !sameAssetRef(bound.assetRef, ref) || bound.indexRevision !== indexRevision) {
        throw new Error("invalid backup archive response");
      }
      return bound.status;
    },
    async issueTicket(token: string, ref: AssetRef, requestId: string, freshStepUpProof: string, signal?: AbortSignal): Promise<BackupExportDownloadTicket> {
      if (!OPAQUE_ID.test(requestId)) throw new Error("invalid backup archive request");
      const raw = await requester(`${basePath(ref)}/archive-member-jobs/${requestId}/delivery-ticket`, {
        method: "POST", token, stepUpProof: stepUpProof(freshStepUpProof), signal, body: { schema_version: 1 },
      });
      return mapTicket(raw);
    },
  };
}

export type BackupArchiveApi = ReturnType<typeof createBackupArchiveApi>;
