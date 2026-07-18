import type {
  AssetRef,
  AssetOverlayState,
  AssetOverlayTombstoneReason,
  AssetSearchRequest,
  BackupAssetFavorite,
  BackupAssetOverlayPage,
  BackupAssetRecentAccess,
  BackupAssetTag,
  BackupAssetTagAssignment,
  CatalogProjection,
  SavedAssetSearch,
  SavedSearchReason,
  SavedSearchState,
} from "@/types/domain";
import {
  blockedBackupAssetProjection,
  isRawBackupAssetObject,
  mapAssetRef,
  mapOpaqueBackupAssetId,
  mapPositiveSafeInteger,
  mapUTCInstant,
  type RawBackupAssetObject,
} from "./backup-assets-boundary";
import { encodeBackupAssetSearchRequest, mapBackupAssetSearchRequest } from "./backup-asset-search-api";
import { request } from "./core";

const savedStates = new Set<SavedSearchState>(["active", "broken", "blocked"]);
const savedReasons = new Set<SavedSearchReason>([
  "point_retired", "point_expiring", "point_expired", "point_failed", "point_purge_blocked",
  "point_missing", "scope_unauthorized", "ast_schema_unsupported",
]);
const overlayStates = new Set<AssetOverlayState>(["active", "tombstone"]);
const tombstoneReasons = new Set<AssetOverlayTombstoneReason>([
  "source_retired", "source_expiring", "source_expired", "source_failed", "source_purge_blocked", "source_missing",
]);

function closedValue<T extends string>(value: unknown, values: Set<T>): T | null {
  return typeof value === "string" && values.has(value as T) ? value as T : null;
}

function optionalInstant(value: unknown): string | null | undefined {
  if (value === undefined || value === null || value === "") return null;
  return mapUTCInstant(value) ?? undefined;
}

export function mapSavedSearch(value: unknown): CatalogProjection<SavedAssetSearch> {
  if (!isRawBackupAssetObject(value)) return blockedBackupAssetProjection();
  const id = mapOpaqueBackupAssetId(value.id);
  const query = mapBackupAssetSearchRequest(value.query);
  const version = mapPositiveSafeInteger(value.version);
  const state = closedValue(value.state, savedStates);
  const reason = value.state_reason === undefined || value.state_reason === "" ? null : closedValue(value.state_reason, savedReasons);
  const brokenAt = optionalInstant(value.broken_at);
  const createdAt = mapUTCInstant(value.created_at);
  const updatedAt = mapUTCInstant(value.updated_at);
  if (id === null || query === null || version === null || state === null || reason === undefined || brokenAt === undefined ||
      createdAt === null || updatedAt === null ||
      (state === "active" && (reason !== null || brokenAt !== null)) ||
      (state === "broken" && (reason === null || reason === "ast_schema_unsupported" || brokenAt === null)) ||
      (state === "blocked" && (reason !== "ast_schema_unsupported" || brokenAt !== null))) {
    return blockedBackupAssetProjection();
  }
  return { status: "available", value: { id, query, version, state, stateReason: reason, brokenAt, createdAt, updatedAt } };
}

function mapOverlayStateProduct(value: RawBackupAssetObject): { state: AssetOverlayState; reason: AssetOverlayTombstoneReason | null } | null {
  const state = closedValue(value.state, overlayStates);
  const reason = value.tombstone_reason === undefined || value.tombstone_reason === "" ? null : closedValue(value.tombstone_reason, tombstoneReasons);
  if (state === null || reason === undefined || (state === "active") !== (reason === null)) return null;
  return { state, reason };
}

export function mapFavorite(value: unknown): CatalogProjection<BackupAssetFavorite> {
  if (!isRawBackupAssetObject(value)) return blockedBackupAssetProjection();
  const id = mapOpaqueBackupAssetId(value.id);
  const ref = mapAssetRef(value.ref);
  const product = mapOverlayStateProduct(value);
  const version = mapPositiveSafeInteger(value.version);
  const createdAt = mapUTCInstant(value.created_at);
  const updatedAt = mapUTCInstant(value.updated_at);
  if (id === null || ref === null || product === null || version === null || createdAt === null || updatedAt === null ||
      (value.label !== undefined && typeof value.label !== "string")) return blockedBackupAssetProjection();
  return {
    status: "available",
    value: { id, ref, label: typeof value.label === "string" ? value.label : "", state: product.state,
      tombstoneReason: product.reason, version, createdAt, updatedAt },
  };
}

export function mapTag(value: unknown): CatalogProjection<BackupAssetTag> {
  if (!isRawBackupAssetObject(value)) return blockedBackupAssetProjection();
  const id = mapOpaqueBackupAssetId(value.id);
  const version = mapPositiveSafeInteger(value.version);
  const createdAt = mapUTCInstant(value.created_at);
  const updatedAt = mapUTCInstant(value.updated_at);
  if (id === null || version === null || createdAt === null || updatedAt === null || typeof value.name !== "string" || value.name === "") {
    return blockedBackupAssetProjection();
  }
  return { status: "available", value: { id, name: value.name, version, createdAt, updatedAt } };
}

export function mapTagAssignment(value: unknown): CatalogProjection<BackupAssetTagAssignment> {
  if (!isRawBackupAssetObject(value)) return blockedBackupAssetProjection();
  const id = mapOpaqueBackupAssetId(value.id);
  const tagId = mapOpaqueBackupAssetId(value.tag_id);
  const ref = mapAssetRef(value.ref);
  const product = mapOverlayStateProduct(value);
  const version = mapPositiveSafeInteger(value.version);
  if (id === null || tagId === null || ref === null || product === null || version === null) return blockedBackupAssetProjection();
  return { status: "available", value: { id, tagId, ref, state: product.state, tombstoneReason: product.reason, version } };
}

export function mapRecentAccess(value: unknown): CatalogProjection<BackupAssetRecentAccess> {
  if (!isRawBackupAssetObject(value)) return blockedBackupAssetProjection();
  const id = mapOpaqueBackupAssetId(value.id);
  const ref = mapAssetRef(value.ref);
  const accessCount = mapPositiveSafeInteger(value.access_count);
  const lastAccessedAt = mapUTCInstant(value.last_accessed_at);
  const expiresAt = mapUTCInstant(value.expires_at);
  const version = mapPositiveSafeInteger(value.version);
  if (
    id === null || ref === null || accessCount === null || lastAccessedAt === null || expiresAt === null || version === null ||
    Date.parse(expiresAt) <= Date.parse(lastAccessedAt)
  ) {
    return blockedBackupAssetProjection();
  }
  return { status: "available", value: { id, ref, accessCount, lastAccessedAt, expiresAt, version } };
}

function mapPage<T>(value: unknown, mapper: (item: unknown) => CatalogProjection<T>): CatalogProjection<BackupAssetOverlayPage<T>> {
  if (!isRawBackupAssetObject(value) || !Array.isArray(value.items)) return blockedBackupAssetProjection();
  const items: T[] = [];
  for (const raw of value.items) {
    const mapped = mapper(raw);
    if (mapped.status !== "available") return blockedBackupAssetProjection();
    items.push(mapped.value);
  }
  const nextCursor = value.next_cursor === undefined || value.next_cursor === null || value.next_cursor === "" ? null : mapOpaqueBackupAssetId(value.next_cursor);
  if (value.next_cursor !== undefined && value.next_cursor !== null && value.next_cursor !== "" && nextCursor === null) return blockedBackupAssetProjection();
  return { status: "available", value: { items, nextCursor } };
}

function encodedRef(ref: AssetRef): RawBackupAssetObject {
  return { recovery_point_id: ref.recoveryPointId, entry_id: ref.entryId };
}

function assertRef(ref: AssetRef): void {
  if (mapAssetRef(encodedRef(ref)) === null) throw new Error("invalid asset reference");
}

function assertID(id: string, name: string): void {
  if (mapOpaqueBackupAssetId(id) === null) throw new Error(`invalid ${name} ID`);
}

function listPath(base: string, limit?: number, cursor?: string): string {
  const parameters: string[] = [];
  if (limit !== undefined) parameters.push(`limit=${encodeURIComponent(String(limit))}`);
  if (cursor !== undefined) {
    assertID(cursor, "overlay cursor");
    parameters.push(`cursor=${encodeURIComponent(cursor)}`);
  }
  return parameters.length === 0 ? base : `${base}?${parameters.join("&")}`;
}

export function createBackupAssetOverlaysApi() {
  return {
    async listSavedSearches(token: string, limit?: number, cursor?: string, signal?: AbortSignal) {
      return mapPage(await request<unknown>(listPath("/asset-saved-searches", limit, cursor), { token, signal }), mapSavedSearch);
    },
    async createSavedSearch(token: string, query: AssetSearchRequest, idempotencyKey: string, signal?: AbortSignal) {
      return mapSavedSearch(await request<unknown>("/asset-saved-searches", {
        method: "POST", token, idempotencyKey, signal, body: { query: encodeBackupAssetSearchRequest(query) },
      }));
    },
    async getSavedSearch(token: string, id: string, signal?: AbortSignal) {
      assertID(id, "saved-search");
      return mapSavedSearch(await request<unknown>(`/asset-saved-searches/${encodeURIComponent(id)}`, { token, signal }));
    },
    async updateSavedSearch(token: string, id: string, query: AssetSearchRequest, expectedVersion: number, idempotencyKey: string, signal?: AbortSignal) {
      assertID(id, "saved-search");
      return mapSavedSearch(await request<unknown>(`/asset-saved-searches/${encodeURIComponent(id)}`, {
        method: "PATCH", token, idempotencyKey, signal,
        body: { query: encodeBackupAssetSearchRequest(query), expected_version: expectedVersion },
      }));
    },
    async deleteSavedSearch(token: string, id: string, expectedVersion: number, idempotencyKey: string, signal?: AbortSignal) {
      assertID(id, "saved-search");
      await request(`/asset-saved-searches/${encodeURIComponent(id)}`, {
        method: "DELETE", token, idempotencyKey, signal, body: { expected_version: expectedVersion },
      });
    },
    async listFavorites(token: string, limit?: number, cursor?: string, signal?: AbortSignal) {
      return mapPage(await request<unknown>(listPath("/asset-favorites", limit, cursor), { token, signal }), mapFavorite);
    },
    async addFavorite(token: string, ref: AssetRef, label: string, idempotencyKey: string, signal?: AbortSignal) {
      assertRef(ref);
      return mapFavorite(await request<unknown>("/asset-favorites", {
        method: "POST", token, idempotencyKey, signal, body: { ref: encodedRef(ref), label },
      }));
    },
    async removeFavorite(token: string, ref: AssetRef, idempotencyKey: string, signal?: AbortSignal) {
      assertRef(ref);
      await request(`/asset-favorites/${encodeURIComponent(ref.recoveryPointId)}/${encodeURIComponent(ref.entryId)}`, {
        method: "DELETE", token, idempotencyKey, signal,
      });
    },
    async listTags(token: string, limit?: number, cursor?: string, signal?: AbortSignal) {
      return mapPage(await request<unknown>(listPath("/asset-tags", limit, cursor), { token, signal }), mapTag);
    },
    async createTag(token: string, name: string, idempotencyKey: string, signal?: AbortSignal) {
      return mapTag(await request<unknown>("/asset-tags", { method: "POST", token, idempotencyKey, signal, body: { name } }));
    },
    async updateTag(token: string, id: string, name: string, expectedVersion: number, idempotencyKey: string, signal?: AbortSignal) {
      assertID(id, "tag");
      return mapTag(await request<unknown>(`/asset-tags/${encodeURIComponent(id)}`, {
        method: "PATCH", token, idempotencyKey, signal, body: { name, expected_version: expectedVersion },
      }));
    },
    async deleteTag(token: string, id: string, expectedVersion: number, idempotencyKey: string, signal?: AbortSignal) {
      assertID(id, "tag");
      await request(`/asset-tags/${encodeURIComponent(id)}`, {
        method: "DELETE", token, idempotencyKey, signal, body: { expected_version: expectedVersion },
      });
    },
    async assignTag(token: string, tagId: string, ref: AssetRef, idempotencyKey: string, signal?: AbortSignal) {
      assertID(tagId, "tag");
      assertRef(ref);
      return mapTagAssignment(await request<unknown>(`/asset-tags/${encodeURIComponent(tagId)}/assignments`, {
        method: "POST", token, idempotencyKey, signal, body: { ref: encodedRef(ref) },
      }));
    },
    async unassignTag(token: string, tagId: string, ref: AssetRef, idempotencyKey: string, signal?: AbortSignal) {
      assertID(tagId, "tag");
      assertRef(ref);
      await request(`/asset-tags/${encodeURIComponent(tagId)}/assignments/${encodeURIComponent(ref.recoveryPointId)}/${encodeURIComponent(ref.entryId)}`, {
        method: "DELETE", token, idempotencyKey, signal,
      });
    },
    async listRecent(token: string, limit?: number, cursor?: string, signal?: AbortSignal) {
      return mapPage(await request<unknown>(listPath("/asset-recent", limit, cursor), { token, signal }), mapRecentAccess);
    },
    async clearRecent(token: string, idempotencyKey: string, signal?: AbortSignal): Promise<number> {
      const raw = await request<unknown>("/asset-recent/clear", { method: "POST", token, idempotencyKey, signal });
      if (!isRawBackupAssetObject(raw)) throw new Error("invalid recent clear response");
      const count = mapPositiveSafeInteger(raw.cleared_count);
      if (count === null && raw.cleared_count !== 0) throw new Error("invalid recent clear count");
      return count ?? 0;
    },
  };
}
