import type {
  AssetRef,
  BackupAsset,
  BackupAssetPage,
  CatalogEntryType,
  CatalogFingerprintStrength,
  CatalogProjection,
  RecoveryPointDiff,
  RecoveryPointDiffChangedField,
  RecoveryPointDiffContentEquality,
  RecoveryPointDiffItem,
  RecoveryPointDiffKind,
  RecoveryPointDiffProviderStatus,
  RecoveryPointDiffSide,
} from "@/types/domain";
import { request } from "./core";
import {
  blockedBackupAssetProjection,
  isRawBackupAssetObject,
  mapAssetRef,
  mapBackupAssetEntryId,
  mapSafeNonNegativeInteger,
} from "./backup-assets-boundary";
import {
  mapCatalogCapabilityReason,
  normalizeNullableCatalogTime,
} from "./recovery-points-api";

export type BackupAssetSort = "name_asc" | "name_desc" | "size_desc" | "modified_desc";
export type RecoveryPointDiffSort = "path_asc";

export interface ListBackupAssetsOptions {
  parent?: AssetRef;
  limit?: number;
  cursor?: string;
  sort?: BackupAssetSort;
  signal?: AbortSignal;
}

export interface RecoveryPointDiffInput {
  baseRecoveryPointId: string;
  compareRecoveryPointId: string;
  baseParent?: AssetRef;
  compareParent?: AssetRef;
  sort?: RecoveryPointDiffSort;
  limit?: number;
  cursor?: string;
}

function blocked<T>(): CatalogProjection<T> {
  return blockedBackupAssetProjection();
}

function finiteInteger(value: unknown): number | null {
  return mapSafeNonNegativeInteger(value);
}

function entryType(value: unknown): CatalogEntryType | null {
  switch (value) {
    case "file":
    case "directory":
    case "symlink":
    case "hardlink":
    case "special":
    case "unknown":
      return value;
    default:
      return null;
  }
}

function fingerprintStrength(value: unknown): CatalogFingerprintStrength | null {
  switch (value) {
    case "strong":
    case "weak":
    case "none":
      return value;
    default:
      return null;
  }
}

function diffKind(value: unknown): RecoveryPointDiffKind | null {
  switch (value) {
    case "added":
    case "removed":
    case "modified":
    case "type_changed":
      return value;
    default:
      return null;
  }
}

function contentEquality(value: unknown): RecoveryPointDiffContentEquality | null {
  switch (value) {
    case "equal":
    case "different":
    case "unknown":
      return value;
    default:
      return null;
  }
}

function providerDiffStatus(value: unknown): RecoveryPointDiffProviderStatus | null {
  switch (value) {
    case "supported":
    case "unavailable":
    case "not_applicable":
      return value;
    default:
      return null;
  }
}

function changedField(value: unknown): RecoveryPointDiffChangedField | null {
  switch (value) {
    case "name":
    case "entry_type":
    case "size":
    case "modified_at":
    case "mode":
    case "owner":
    case "mime_type":
    case "fingerprint_strength":
    case "content":
      return value;
    default:
      return null;
  }
}

function mapBreadcrumb(value: unknown, recoveryPointId: string) {
  if (!Array.isArray(value)) {
    return [];
  }
  const result = [];
  for (const item of value) {
    if (!isRawBackupAssetObject(item)) {
      return null;
    }
    const ref = mapAssetRef(item);
    if (ref === null || ref.recoveryPointId !== recoveryPointId || typeof item.name !== "string" || item.name === "") {
      return null;
    }
    result.push({ ref, name: item.name });
  }
  return result;
}

export function mapBackupAsset(value: unknown): CatalogProjection<BackupAsset> {
  if (!isRawBackupAssetObject(value)) {
    return blocked();
  }
  const ref = mapAssetRef(value);
  const mappedEntryType = entryType(value.entry_type);
  const size = finiteInteger(value.size);
  const strength = fingerprintStrength(value.fingerprint_strength);
  if (ref === null || mappedEntryType === null || size === null || strength === null ||
    typeof value.name !== "string" || value.name === "") {
    return blocked();
  }
  let parentRef: AssetRef | null = null;
  if (value.parent_entry_id !== null && value.parent_entry_id !== undefined && value.parent_entry_id !== "") {
    const parentEntryId = mapBackupAssetEntryId(value.parent_entry_id);
    if (parentEntryId === null) {
      return blocked();
    }
    parentRef = { recoveryPointId: ref.recoveryPointId, entryId: parentEntryId };
  }
  const breadcrumb = mapBreadcrumb(value.breadcrumb, ref.recoveryPointId);
  if (breadcrumb === null) {
    return blocked();
  }
  return {
    status: "available",
    value: {
      ref,
      parentRef,
      name: value.name,
      entryType: mappedEntryType,
      size,
      modifiedAt: normalizeNullableCatalogTime(value.modified_at),
      mode: typeof value.mode === "string" ? value.mode : "",
      owner: typeof value.owner === "string" ? value.owner : "",
      mimeType: typeof value.mime_type === "string" ? value.mime_type : "",
      fingerprintStrength: strength,
      breadcrumb,
    },
  };
}

function mapDiffSide(value: unknown): RecoveryPointDiffSide | null | undefined {
  if (value === null || value === undefined) {
    return null;
  }
  if (!isRawBackupAssetObject(value)) {
    return undefined;
  }
  const ref = mapAssetRef(value);
  const mappedEntryType = entryType(value.entry_type);
  const size = finiteInteger(value.size);
  const strength = fingerprintStrength(value.fingerprint_strength);
  if (ref === null || mappedEntryType === null || size === null || strength === null ||
    typeof value.name !== "string" || value.name === "") {
    return undefined;
  }
  return {
    ref,
    name: value.name,
    entryType: mappedEntryType,
    size,
    modifiedAt: normalizeNullableCatalogTime(value.modified_at),
    mode: typeof value.mode === "string" ? value.mode : "",
    owner: typeof value.owner === "string" ? value.owner : "",
    mimeType: typeof value.mime_type === "string" ? value.mime_type : "",
    fingerprintStrength: strength,
  };
}

function mapDiffItem(value: unknown): RecoveryPointDiffItem | null {
  if (!isRawBackupAssetObject(value)) {
    return null;
  }
  const kind = diffKind(value.kind);
  const equality = contentEquality(value.content_equality);
  const base = mapDiffSide(value.base);
  const compare = mapDiffSide(value.compare);
  if (kind === null || equality === null || base === undefined || compare === undefined ||
    (kind === "added" && (base !== null || compare === null)) ||
    (kind === "removed" && (base === null || compare !== null)) ||
    ((kind === "modified" || kind === "type_changed") && (base === null || compare === null)) ||
    !Array.isArray(value.changed_fields)) {
    return null;
  }
  const changedFields: RecoveryPointDiffChangedField[] = [];
  for (const rawField of value.changed_fields) {
    const field = changedField(rawField);
    if (field === null) {
      return null;
    }
    changedFields.push(field);
  }
  return { kind, base, compare, changedFields, contentEquality: equality };
}

export function mapRecoveryPointDiff(value: unknown): CatalogProjection<RecoveryPointDiff> {
  if (!isRawBackupAssetObject(value) || !Array.isArray(value.items) || !isRawBackupAssetObject(value.provider_evidence)) {
    return blocked();
  }
  const providerStatus = providerDiffStatus(value.provider_evidence.status);
  if (providerStatus === null) {
    return blocked();
  }
  const items: RecoveryPointDiffItem[] = [];
  for (const rawItem of value.items) {
    const item = mapDiffItem(rawItem);
    if (item === null) {
      return blocked();
    }
    items.push(item);
  }
  return {
    status: "available",
    value: {
      items,
      nextCursor: typeof value.next_cursor === "string" && value.next_cursor !== "" ? value.next_cursor : null,
      providerEvidence: {
        status: providerStatus,
        reason: mapCatalogCapabilityReason(value.provider_evidence.reason),
      },
    },
  };
}

function mapBackupAssetPage(value: unknown): BackupAssetPage {
  const raw = isRawBackupAssetObject(value) ? value : {};
  return {
    items: Array.isArray(raw.items) ? raw.items.map(mapBackupAsset) : [],
    nextCursor: typeof raw.next_cursor === "string" && raw.next_cursor !== "" ? raw.next_cursor : null,
  };
}

function appendQuery(path: string, query: URLSearchParams): string {
  const encoded = query.toString();
  return encoded === "" ? path : `${path}?${encoded}`;
}

function assertScopedParent(parent: AssetRef | undefined, recoveryPointId: string): void {
  if (parent !== undefined && parent.recoveryPointId !== recoveryPointId) {
    throw new Error("asset reference recovery point mismatch");
  }
}

export function createBackupAssetsApi() {
  return {
    async listBackupAssets(
      token: string,
      recoveryPointId: string,
      options: ListBackupAssetsOptions = {},
    ): Promise<BackupAssetPage> {
      assertScopedParent(options.parent, recoveryPointId);
      const query = new URLSearchParams();
      if (options.parent) query.set("parent", options.parent.entryId);
      if (options.limit !== undefined) query.set("limit", String(options.limit));
      if (options.cursor) query.set("cursor", options.cursor);
      if (options.sort) query.set("sort", options.sort);
      const raw = await request<unknown>(
        appendQuery(`/recovery-points/${encodeURIComponent(recoveryPointId)}/entries`, query),
        { token, signal: options.signal },
      );
      return mapBackupAssetPage(raw);
    },

    async getBackupAsset(
      token: string,
      ref: AssetRef,
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupAsset>> {
      const raw = await request<unknown>(
        `/recovery-points/${encodeURIComponent(ref.recoveryPointId)}/entries/${encodeURIComponent(ref.entryId)}`,
        { token, signal },
      );
      return mapBackupAsset(raw);
    },

    async diffRecoveryPoints(
      token: string,
      input: RecoveryPointDiffInput,
      signal?: AbortSignal,
    ): Promise<CatalogProjection<RecoveryPointDiff>> {
      assertScopedParent(input.baseParent, input.baseRecoveryPointId);
      assertScopedParent(input.compareParent, input.compareRecoveryPointId);
      const body: Record<string, string | number> = {
        base_recovery_point_id: input.baseRecoveryPointId,
        compare_recovery_point_id: input.compareRecoveryPointId,
      };
      if (input.baseParent) body.base_parent_entry_id = input.baseParent.entryId;
      if (input.compareParent) body.compare_parent_entry_id = input.compareParent.entryId;
      if (input.sort) body.sort = input.sort;
      if (input.limit !== undefined) body.limit = input.limit;
      if (input.cursor) body.cursor = input.cursor;
      const raw = await request<unknown>("/recovery-point-diffs", {
        method: "POST",
        token,
        signal,
        body,
      });
      return mapRecoveryPointDiff(raw);
    },
  };
}
