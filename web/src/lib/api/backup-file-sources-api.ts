import type {
  BackupFileSourceNode,
  BackupFileSourcePage,
  BackupFileSourceBrowseState,
  BackupFileSourceRecoveryPoint,
  BackupFileSourceSet,
  BackupFileSourceVersion,
  CatalogCapabilityCode,
  CatalogCapabilityReason,
  CatalogCoverageStatus,
  CatalogProjection,
} from "@/types/domain";

import { request } from "./core";

type RawObject = Record<string, unknown>;

export interface ListBackupFileSourcesOptions {
  limit?: number;
  cursor?: string;
  signal?: AbortSignal;
}

export interface ResolveBackupFileSourceOptions {
  signal?: AbortSignal;
}

const BLOCKED_REASON: CatalogCapabilityReason = { code: "unknown_internal_state", params: {} };
const OPAQUE_ID = /^[0-9a-f]{32}$/;
const CURSOR = /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/;
const UTC_INSTANT = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?Z$/;
const CAPABILITY_CODES = new Set<CatalogCapabilityCode>([
  "feature_disabled", "task_artifact_contract_missing", "repository_offline", "repository_disconnected",
  "provider_unavailable", "repository_identity_unavailable", "provider_protocol_incompatible",
  "provider_operation_timeout", "provider_resource_limit", "point_not_committed", "mutable_source_changed",
  "catalog_unavailable", "sequential_read_unavailable", "range_unavailable", "download_unavailable",
  "restore_unavailable", "diff_unavailable", "unknown_internal_state",
]);
const CAPABILITY_PARAMS = new Set([
  "provider_kind", "repository_status", "recovery_point_state", "capability", "correlation_id",
  "retry_after_seconds",
]);

function isObject(value: unknown): value is RawObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function blocked<T>(): CatalogProjection<T> {
  return { status: "blocked", reason: BLOCKED_REASON };
}

function integer(value: unknown, minimum = 0): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum ? value : null;
}

function label(value: unknown): string | null {
  return typeof value === "string" && value === value.trim() && value.length > 0 && value.length <= 255 && !/[\r\n\0]/.test(value)
    ? value
    : null;
}

function time(value: unknown, nullable: boolean): string | null | undefined {
  if (nullable && value === null) return null;
  if (typeof value !== "string" || value.length > 64) return undefined;
  const match = UTC_INSTANT.exec(value);
  const milliseconds = Date.parse(value);
  if (!match || !Number.isFinite(milliseconds)) return undefined;
  const expectedSecond = `${match[1]}-${match[2]}-${match[3]}T${match[4]}:${match[5]}:${match[6]}`;
  return new Date(milliseconds).toISOString().slice(0, 19) === expectedSecond ? value : undefined;
}

function coverage(value: unknown): CatalogCoverageStatus | null {
  return value === "building" || value === "complete" || value === "partial" || value === "failed" || value === "unavailable"
    ? value
    : null;
}

function opaqueId(value: unknown): string | null {
  return typeof value === "string" && OPAQUE_ID.test(value) ? value : null;
}

function reason(value: unknown): CatalogCapabilityReason | null | undefined {
  if (value === null) return null;
  if (!isObject(value) || typeof value.code !== "string" || !CAPABILITY_CODES.has(value.code as CatalogCapabilityCode) || !isObject(value.params)) {
    return undefined;
  }
  const params: Record<string, string> = {};
  for (const [key, raw] of Object.entries(value.params)) {
    if (!CAPABILITY_PARAMS.has(key) || typeof raw !== "string" || raw.length > 128 || /[\r\n\0]/.test(raw)) return undefined;
    if (key === "retry_after_seconds" && !/^[1-9][0-9]*$/.test(raw)) return undefined;
    params[key] = raw;
  }
  return { code: value.code as CatalogCapabilityCode, params };
}

function browseState(value: unknown): BackupFileSourceBrowseState | null {
  return value === "browsable" || value === "indexing" || value === "unavailable" ? value : null;
}

function unavailableReason(
  value: unknown,
  state: BackupFileSourceBrowseState | null,
): CatalogCapabilityReason | null | undefined {
  if (state === null) return undefined;
  if (state !== "unavailable") return value === undefined || value === null ? null : undefined;
  if (!isObject(value) || typeof value.code !== "string" || !CAPABILITY_CODES.has(value.code as CatalogCapabilityCode)) {
    return undefined;
  }
  if (value.params !== undefined && (!isObject(value.params) || Object.keys(value.params).length > 0)) return undefined;
  return { code: value.code as CatalogCapabilityCode, params: {} };
}

function cursor(value: unknown): string | null | undefined {
  if (value === undefined || value === null || value === "") return null;
  return typeof value === "string" && value.length <= 8192 && CURSOR.test(value) ? value : undefined;
}

function mapPage<T>(
  value: unknown,
  mapItem: (value: unknown) => T | null,
  identity: (item: T) => string,
): CatalogProjection<BackupFileSourcePage<T>> {
  if (!isObject(value) || !Array.isArray(value.items)) return blocked();
  const nextCursor = cursor(value.next_cursor);
  if (nextCursor === undefined) return blocked();
  const items: T[] = [];
  const identities = new Set<string>();
  for (const raw of value.items) {
    const item = mapItem(raw);
    if (item === null || identities.has(identity(item))) return blocked();
    identities.add(identity(item));
    items.push(item);
  }
  return { status: "available", value: { items, nextCursor } };
}

function mapNode(value: unknown): BackupFileSourceNode | null {
  if (!isObject(value)) return null;
  const nodeId = integer(value.node_id, 1);
  const displayName = label(value.display_name);
  const backupSetCount = integer(value.backup_set_count, 1);
  const retainedVersionCount = integer(value.retained_version_count, 1);
  const latestRetainedAt = time(value.latest_retained_at, true);
  const catalogCoverage = coverage(value.catalog_coverage);
  const state = browseState(value.browse_state);
  const unavailable = unavailableReason(value.unavailable_reason, state);
  return nodeId === null || displayName === null || backupSetCount === null || retainedVersionCount === null ||
    retainedVersionCount < backupSetCount || latestRetainedAt === undefined || catalogCoverage === null || state === null || unavailable === undefined
    ? null
    : { nodeId, displayName, backupSetCount, retainedVersionCount, latestRetainedAt, catalogCoverage, browseState: state, unavailableReason: unavailable };
}

function mapSet(value: unknown): BackupFileSourceSet | null {
  if (!isObject(value)) return null;
  const backupSetId = opaqueId(value.backup_set_id);
  const nodeId = integer(value.node_id, 1);
  const displayLabel = label(value.display_label);
  const lineageKind = value.lineage_kind === "task" || value.lineage_kind === "imported" ? value.lineage_kind : null;
  const versionCount = integer(value.version_count, 1);
  const latestRetainedAt = time(value.latest_retained_at, true);
  const catalogCoverage = coverage(value.catalog_coverage);
  const state = browseState(value.browse_state);
  const unavailable = unavailableReason(value.unavailable_reason, state);
  return backupSetId === null || nodeId === null || displayLabel === null || lineageKind === null || versionCount === null ||
    latestRetainedAt === undefined || catalogCoverage === null || state === null || unavailable === undefined
    ? null
    : { backupSetId, nodeId, displayLabel, lineageKind, versionCount, latestRetainedAt, catalogCoverage, browseState: state, unavailableReason: unavailable };
}

function mapVersion(value: unknown): BackupFileSourceVersion | null {
  if (!isObject(value)) return null;
  const recoveryPointId = opaqueId(value.recovery_point_id);
  const repositoryId = opaqueId(value.repository_id);
  const producingTaskId = value.producing_task_id === undefined ? undefined : integer(value.producing_task_id, 1);
  const capturedAt = time(value.captured_at, true);
  const committedAt = time(value.committed_at, true);
  const createdAt = time(value.created_at, false);
  const lifecycleState = value.lifecycle_state === "observed" || value.lifecycle_state === "verifying" ||
    value.lifecycle_state === "committed" || value.lifecycle_state === "degraded"
    ? value.lifecycle_state
    : null;
  const catalogCoverage = coverage(value.catalog_coverage);
  const state = browseState(value.browse_state);
  const unavailable = unavailableReason(value.unavailable_reason, state);
  const entryCount = integer(value.entry_count);
  const logicalBytes = integer(value.logical_bytes);
  if (!isObject(value.permissions) || value.permissions.list !== true || value.permissions.preview !== false || value.permissions.download !== false) return null;
  if (!isObject(value.content_availability) || typeof value.content_availability.available !== "boolean") return null;
  const availabilityReason = reason(value.content_availability.reason);
  if (availabilityReason === undefined || (value.content_availability.available && availabilityReason !== null) ||
    (!value.content_availability.available && availabilityReason === null) ||
    (state !== "unavailable" && !value.content_availability.available)) return null;
  if (recoveryPointId === null || repositoryId === null || producingTaskId === null || capturedAt === undefined ||
    committedAt === undefined || createdAt === undefined || createdAt === null || lifecycleState === null || catalogCoverage === null ||
    state === null || unavailable === undefined || entryCount === null || logicalBytes === null) return null;
  return {
    recoveryPointId, repositoryId, producingTaskId, capturedAt, committedAt, createdAt, lifecycleState,
    catalogCoverage, contentAvailability: { available: value.content_availability.available, reason: availabilityReason },
    browseState: state, unavailableReason: unavailable,
    entryCount, logicalBytes, permissions: { list: true, preview: false, download: false },
  };
}

export const mapBackupFileSourceNodePage = (value: unknown) => mapPage(value, mapNode, (item) => String(item.nodeId));
export const mapBackupFileSourceSetPage = (value: unknown) => mapPage(value, mapSet, (item) => item.backupSetId);
export const mapBackupFileSourceVersionPage = (value: unknown) => mapPage(value, mapVersion, (item) => item.recoveryPointId);

export function mapBackupFileSourceRecoveryPoint(value: unknown): CatalogProjection<BackupFileSourceRecoveryPoint> {
  if (!isObject(value)) return blocked();
  const nodeId = integer(value.node_id, 1);
  const backupSetId = opaqueId(value.backup_set_id);
  const recoveryPointId = opaqueId(value.recovery_point_id);
  const repositoryId = opaqueId(value.repository_id);
  const producingTaskId = value.producing_task_id === undefined ? undefined : integer(value.producing_task_id, 1);
  const state = browseState(value.browse_state);
  const unavailable = unavailableReason(value.unavailable_reason, state);
  if (nodeId === null || backupSetId === null || recoveryPointId === null || repositoryId === null || producingTaskId === null ||
    state === null || unavailable === undefined) {
    return blocked();
  }
  return {
    status: "available",
    value: { nodeId, backupSetId, recoveryPointId, repositoryId, producingTaskId, browseState: state, unavailableReason: unavailable },
  };
}

function queryPath(path: string, options: ListBackupFileSourcesOptions): string {
  const query = new URLSearchParams();
  if (options.limit !== undefined) {
    if (!Number.isSafeInteger(options.limit) || options.limit < 1 || options.limit > 100) throw new Error("invalid page limit");
    query.set("limit", String(options.limit));
  }
  if (options.cursor !== undefined) {
    if (cursor(options.cursor) === undefined || options.cursor === "") throw new Error("invalid cursor");
    query.set("cursor", options.cursor);
  }
  return query.size === 0 ? path : `${path}?${query.toString()}`;
}

export function createBackupFileSourcesApi() {
  return {
    async resolveBackupFileSourceRecoveryPoint(token: string, recoveryPointId: string, options: ResolveBackupFileSourceOptions = {}) {
      if (!OPAQUE_ID.test(recoveryPointId)) throw new Error("invalid recovery point id");
      const resolved = mapBackupFileSourceRecoveryPoint(await request<unknown>(
        `/backup-file-sources/recovery-points/${recoveryPointId}/source`,
        { token, signal: options.signal },
      ));
      return resolved.status === "available" && resolved.value.recoveryPointId !== recoveryPointId
        ? blocked<BackupFileSourceRecoveryPoint>()
        : resolved;
    },
    async listBackupFileSourceNodes(token: string, options: ListBackupFileSourcesOptions = {}) {
      return mapBackupFileSourceNodePage(await request<unknown>(queryPath("/backup-file-sources/nodes", options), { token, signal: options.signal }));
    },
    async listBackupFileSourceSets(token: string, nodeId: number, options: ListBackupFileSourcesOptions = {}) {
      if (!Number.isSafeInteger(nodeId) || nodeId < 1) throw new Error("invalid node id");
      const page = mapBackupFileSourceSetPage(await request<unknown>(queryPath(`/backup-file-sources/nodes/${nodeId}/sets`, options), { token, signal: options.signal }));
      return page.status === "available" && page.value.items.some((item) => item.nodeId !== nodeId)
        ? blocked<BackupFileSourcePage<BackupFileSourceSet>>()
        : page;
    },
    async listBackupFileSourceVersions(token: string, backupSetId: string, options: ListBackupFileSourcesOptions = {}) {
      if (!OPAQUE_ID.test(backupSetId)) throw new Error("invalid backup set id");
      return mapBackupFileSourceVersionPage(await request<unknown>(queryPath(`/backup-file-sources/sets/${backupSetId}/versions`, options), { token, signal: options.signal }));
    },
  };
}
