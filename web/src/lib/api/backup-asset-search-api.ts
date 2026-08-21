import type {
  AssetSearchCoverageStatus,
  AssetSearchField,
  AssetSearchHit,
  AssetSearchHitField,
  AssetSearchIndexStatus,
  AssetSearchQueryNode,
  AssetSearchRequest,
  AssetSearchResponse,
  AssetSearchScope,
  AssetSearchSort,
  AssetSearchStalenessStatus,
  AssetSearchSuggestion,
  AssetSearchTotalRelation,
  CatalogEntryType,
  CatalogProjection,
} from "@/types/domain";
import {
  blockedBackupAssetProjection,
  isRawBackupAssetObject,
  mapAssetRef,
  mapOpaqueBackupAssetId,
  mapPositiveSafeInteger,
  mapSafeNonNegativeInteger,
  mapUTCInstant,
  type RawBackupAssetObject,
} from "./backup-assets-boundary";
import { mapBackupAsset } from "./backup-assets-api";
import { request } from "./core";

const searchFields = new Set<AssetSearchField>(["any", "name", "path", "extension", "tag", "content", "ocr"]);
const hitFields = new Set<AssetSearchHitField>(["name", "path", "extension", "tag", "content", "ocr", "type", "modified_time"]);
const entryTypes = new Set<CatalogEntryType>(["file", "directory", "symlink", "hardlink", "special", "unknown"]);
const searchSorts = new Set<AssetSearchSort>(["relevance", "name_asc", "modified_desc"]);
const coverageStates = new Set<AssetSearchCoverageStatus>(["complete", "partial", "building", "failed", "unavailable"]);
const stalenessStates = new Set<AssetSearchStalenessStatus>(["fresh", "stale", "unknown"]);
const totalRelations = new Set<AssetSearchTotalRelation>(["exact", "lower_bound", "unavailable"]);

type QueryState = { nodes: number };

function onlyKeys(value: RawBackupAssetObject, allowed: readonly string[]): boolean {
  const accepted = new Set(allowed);
  return Object.keys(value).every((key) => accepted.has(key));
}

function closedValue<T extends string>(value: unknown, values: Set<T>): T | null {
  return typeof value === "string" && values.has(value as T) ? value as T : null;
}

function mapQueryNode(value: unknown, state: QueryState, depth: number): AssetSearchQueryNode | null {
  if (!isRawBackupAssetObject(value) || depth > 8 || ++state.nodes > 64 || typeof value.op !== "string") return null;
  switch (value.op) {
    case "and":
    case "or": {
      if (!onlyKeys(value, ["op", "children"]) || !Array.isArray(value.children) || value.children.length < 2) return null;
      const children: AssetSearchQueryNode[] = [];
      for (const child of value.children) {
        const mapped = mapQueryNode(child, state, depth + 1);
        if (mapped === null) return null;
        children.push(mapped);
      }
      return { op: value.op, children };
    }
    case "not": {
      if (!onlyKeys(value, ["op", "children"]) || !Array.isArray(value.children) || value.children.length !== 1) return null;
      const child = mapQueryNode(value.children[0], state, depth + 1);
      return child === null ? null : { op: "not", children: [child] };
    }
    case "term": {
      const field = closedValue(value.field, searchFields);
      return onlyKeys(value, ["op", "field", "text"]) && field !== null && typeof value.text === "string" && value.text !== ""
        ? { op: "term", field, text: value.text }
        : null;
    }
    case "type": {
      if (!onlyKeys(value, ["op", "values"]) || !Array.isArray(value.values) || value.values.length === 0) return null;
      const values: CatalogEntryType[] = [];
      for (const raw of value.values) {
        const mapped = closedValue(raw, entryTypes);
        if (mapped === null || values.includes(mapped)) return null;
        values.push(mapped);
      }
      return { op: "type", values };
    }
    case "modified_time": {
      if (!onlyKeys(value, ["op", "from", "to"])) return null;
      const from = value.from === null || value.from === undefined ? null : mapUTCInstant(value.from);
      const to = value.to === null || value.to === undefined ? null : mapUTCInstant(value.to);
      if ((value.from !== null && value.from !== undefined && from === null) ||
          (value.to !== null && value.to !== undefined && to === null) ||
          (from === null && to === null) || (from !== null && to !== null && from > to)) return null;
      return { op: "modified_time", from, to };
    }
    default:
      return null;
  }
}

function mapStringArray(value: unknown, mapItem: (item: unknown) => string | null): string[] | null {
  if (value === undefined) return [];
  if (!Array.isArray(value)) return null;
  const result: string[] = [];
  for (const item of value) {
    const mapped = mapItem(item);
    if (mapped === null || result.includes(mapped)) return null;
    result.push(mapped);
  }
  return result;
}

function mapTaskIDs(value: unknown): number[] | null {
  if (value === undefined) return [];
  if (!Array.isArray(value)) return null;
  const result: number[] = [];
  for (const item of value) {
    const mapped = mapPositiveSafeInteger(item);
    if (mapped === null || result.includes(mapped)) return null;
    result.push(mapped);
  }
  return result;
}

function mapScope(value: unknown): AssetSearchScope | null {
  if (!isRawBackupAssetObject(value) || !onlyKeys(value, ["mode", "repository_ids", "task_ids", "recovery_point_ids"])) return null;
  const mode = closedValue(value.mode, new Set<AssetSearchScope["mode"]>(["current", "all_retained", "exact_points"]));
  const repositoryIds = mapStringArray(value.repository_ids, mapOpaqueBackupAssetId);
  const taskIds = mapTaskIDs(value.task_ids);
  const recoveryPointIds = mapStringArray(value.recovery_point_ids, mapOpaqueBackupAssetId);
  if (mode === null || repositoryIds === null || taskIds === null || recoveryPointIds === null) return null;
  if ((mode === "exact_points") !== (recoveryPointIds.length > 0) || (mode === "exact_points" && (repositoryIds.length > 0 || taskIds.length > 0))) return null;
  return { mode, repositoryIds, taskIds, recoveryPointIds };
}

export function mapBackupAssetSearchRequest(value: unknown): AssetSearchRequest | null {
  if (!isRawBackupAssetObject(value) || !onlyKeys(value, ["schema_version", "root", "scope", "sort", "limit", "cursor"]) || value.schema_version !== 1) return null;
  const root = mapQueryNode(value.root, { nodes: 0 }, 1);
  const scope = mapScope(value.scope);
  const sort = closedValue(value.sort, searchSorts);
  const limit = mapPositiveSafeInteger(value.limit);
  const cursor = value.cursor === undefined || value.cursor === null || value.cursor === "" ? null : value.cursor;
  if (root === null || scope === null || sort === null || limit === null || limit > 200 ||
      (cursor !== null && (typeof cursor !== "string" || cursor.length > 8192))) return null;
  return { schemaVersion: 1, root, scope, sort, limit, cursor };
}

function encodeQueryNode(node: AssetSearchQueryNode): RawBackupAssetObject {
  switch (node.op) {
    case "and":
    case "or":
      return { op: node.op, children: node.children.map(encodeQueryNode) };
    case "not":
      return { op: "not", children: node.children.map(encodeQueryNode) };
    case "term":
      return { op: "term", field: node.field, text: node.text };
    case "type":
      return { op: "type", values: node.values };
    case "modified_time": {
      const result: RawBackupAssetObject = { op: "modified_time" };
      if (node.from !== null) result.from = node.from;
      if (node.to !== null) result.to = node.to;
      return result;
    }
  }
}

export function encodeBackupAssetSearchRequest(value: AssetSearchRequest): RawBackupAssetObject {
  const scope: RawBackupAssetObject = { mode: value.scope.mode };
  if (value.scope.repositoryIds.length > 0) scope.repository_ids = value.scope.repositoryIds;
  if (value.scope.taskIds.length > 0) scope.task_ids = value.scope.taskIds;
  if (value.scope.recoveryPointIds.length > 0) scope.recovery_point_ids = value.scope.recoveryPointIds;
  const result: RawBackupAssetObject = {
    schema_version: value.schemaVersion,
    root: encodeQueryNode(value.root),
    scope,
    sort: value.sort,
    limit: value.limit,
  };
  if (value.cursor) result.cursor = value.cursor;
  return result;
}

function mapIndex(value: unknown): AssetSearchIndexStatus | null {
  if (!isRawBackupAssetObject(value)) return null;
  const recoveryPointId = mapOpaqueBackupAssetId(value.recovery_point_id);
  const catalogGenerationId = value.catalog_generation_id === undefined || value.catalog_generation_id === "" ? null : mapOpaqueBackupAssetId(value.catalog_generation_id);
  const searchGenerationId = value.search_generation_id === undefined || value.search_generation_id === "" ? null : mapOpaqueBackupAssetId(value.search_generation_id);
  const projectionRevision = mapSafeNonNegativeInteger(value.projection_revision);
  const coverage = closedValue(value.coverage, coverageStates);
  const staleness = closedValue(value.staleness, stalenessStates);
  if (recoveryPointId === null || projectionRevision === null || coverage === null || staleness === null ||
      (value.catalog_generation_id !== undefined && value.catalog_generation_id !== "" && catalogGenerationId === null) ||
      (value.search_generation_id !== undefined && value.search_generation_id !== "" && searchGenerationId === null) ||
      (coverage === "complete" && (catalogGenerationId === null || searchGenerationId === null || projectionRevision <= 0))) return null;
  return { recoveryPointId, catalogGenerationId, searchGenerationId, projectionRevision, coverage, staleness };
}

function mapHit(value: unknown, indexes: Map<string, AssetSearchIndexStatus>, allowContent: boolean): AssetSearchHit | null {
  if (!isRawBackupAssetObject(value) || !Array.isArray(value.hit_fields)) return null;
  const ref = mapAssetRef(value.ref);
  const assetProjection = mapBackupAsset(value.asset);
  const score = mapSafeNonNegativeInteger(value.score);
  const index = ref === null ? undefined : indexes.get(ref.recoveryPointId);
  if (ref === null || assetProjection.status !== "available" || score === null ||
      assetProjection.value.ref.recoveryPointId !== ref.recoveryPointId || assetProjection.value.ref.entryId !== ref.entryId ||
      index === undefined || index.catalogGenerationId === null || index.searchGenerationId === null || index.projectionRevision <= 0 ||
      (index.coverage !== "complete" && index.coverage !== "partial")) return null;
  const mappedFields: AssetSearchHitField[] = [];
  for (const raw of value.hit_fields) {
    const field = closedValue(raw, hitFields);
    if (field === null || mappedFields.includes(field) || ((!allowContent) && (field === "content" || field === "ocr"))) return null;
    mappedFields.push(field);
  }
  if (mappedFields.length === 0) return null;
  let snippet: AssetSearchHit["snippet"] = null;
  if (value.snippet !== undefined && value.snippet !== null) {
    if (!allowContent || !isRawBackupAssetObject(value.snippet) ||
        (value.snippet.field !== "content" && value.snippet.field !== "ocr") ||
        !mappedFields.includes(value.snippet.field) || typeof value.snippet.text !== "string" || value.snippet.text === "") return null;
    snippet = { field: value.snippet.field, text: value.snippet.text };
  }
  let retainedVersionCount: number | undefined;
  if (value.retained_version_count !== undefined) {
    const mappedCount = mapPositiveSafeInteger(value.retained_version_count);
    if (mappedCount === null) return null;
    retainedVersionCount = mappedCount;
  }
  return {
    ref,
    asset: assetProjection.value,
    hitFields: mappedFields,
    score,
    snippet,
    ...(retainedVersionCount === undefined ? {} : { retainedVersionCount }),
  };
}

function mapSuggestion(value: unknown, allowContent: boolean): AssetSearchSuggestion | null {
  if (!isRawBackupAssetObject(value)) return null;
  const field = closedValue(value.field, hitFields);
  return field !== null && (allowContent || (field !== "content" && field !== "ocr")) &&
    typeof value.value === "string" && value.value !== "" ? { field, value: value.value } : null;
}

export function mapBackupAssetSearch(value: unknown): CatalogProjection<AssetSearchResponse> {
  if (!isRawBackupAssetObject(value) || typeof value.query_generation !== "string" || !/^[0-9a-f]{64}$/.test(value.query_generation) ||
      !Array.isArray(value.indexes) || !Array.isArray(value.items) || !Array.isArray(value.suggestions) ||
      !isRawBackupAssetObject(value.coverage) || !isRawBackupAssetObject(value.capabilities) || !isRawBackupAssetObject(value.permissions)) {
    return blockedBackupAssetProjection();
  }
  const coverage = closedValue(value.coverage.status, coverageStates);
  const totalRelation = closedValue(value.total_relation, totalRelations);
  const total = value.total === null ? null : mapSafeNonNegativeInteger(value.total);
  if (coverage === null || totalRelation === null || (value.total !== null && total === null) ||
      typeof value.authoritative_empty !== "boolean" || typeof value.capabilities.metadata !== "boolean" ||
      typeof value.capabilities.content !== "boolean" || typeof value.permissions.list !== "boolean" ||
      typeof value.permissions.secret_reveal !== "boolean" || !value.capabilities.metadata || !value.permissions.list) {
    return blockedBackupAssetProjection();
  }
  const indexes = new Map<string, AssetSearchIndexStatus>();
  for (const raw of value.indexes) {
    const mapped = mapIndex(raw);
    if (mapped === null || indexes.has(mapped.recoveryPointId)) return blockedBackupAssetProjection();
    indexes.set(mapped.recoveryPointId, mapped);
  }
  if (coverage === "complete" && [...indexes.values()].some((index) => index.coverage !== "complete")) {
    return blockedBackupAssetProjection();
  }
  const allowContent = value.capabilities.content;
  const items: AssetSearchHit[] = [];
  for (const raw of value.items) {
    const mapped = mapHit(raw, indexes, allowContent);
    if (mapped === null) return blockedBackupAssetProjection();
    items.push(mapped);
  }
  const suggestions: AssetSearchSuggestion[] = [];
  for (const raw of value.suggestions) {
    const mapped = mapSuggestion(raw, allowContent);
    if (mapped === null) return blockedBackupAssetProjection();
    suggestions.push(mapped);
  }
  if ((coverage === "complete" && (totalRelation !== "exact" || total === null || total < items.length || value.authoritative_empty !== (total === 0))) ||
      (coverage !== "complete" && (value.authoritative_empty || (total === null ? totalRelation !== "unavailable" : totalRelation !== "lower_bound" || total < items.length)))) {
    return blockedBackupAssetProjection();
  }
  const nextCursor = value.next_cursor === undefined || value.next_cursor === null || value.next_cursor === "" ? null : value.next_cursor;
  if (nextCursor !== null && (typeof nextCursor !== "string" || nextCursor.length > 8192)) return blockedBackupAssetProjection();
  return {
    status: "available",
    value: {
      queryGeneration: value.query_generation,
      indexes: [...indexes.values()],
      items,
      nextCursor,
      total,
      totalRelation,
      authoritativeEmpty: value.authoritative_empty,
      coverage: { status: coverage },
      suggestions,
      capabilities: { metadata: value.capabilities.metadata, content: value.capabilities.content },
      permissions: { list: value.permissions.list, secretReveal: value.permissions.secret_reveal },
    },
  };
}

export type BackupAssetSearchInput =
  | { query: AssetSearchRequest; savedSearchId?: never; limit?: never; cursor?: never; secretRevealProof?: string; signal?: AbortSignal }
  | { query?: never; savedSearchId: string; limit?: number; cursor?: string; secretRevealProof?: string; signal?: AbortSignal };

export function createBackupAssetSearchApi() {
  return {
    async search(token: string, input: BackupAssetSearchInput): Promise<CatalogProjection<AssetSearchResponse>> {
      const body: RawBackupAssetObject = input.query
        ? { query: encodeBackupAssetSearchRequest(input.query) }
        : { saved_search_id: input.savedSearchId };
      if (!input.query) {
        if (mapOpaqueBackupAssetId(input.savedSearchId) === null) throw new Error("invalid saved-search ID");
        if (input.limit !== undefined) body.limit = input.limit;
        if (input.cursor) body.cursor = input.cursor;
      }
      const raw = await request<unknown>("/asset-search", {
        method: "POST",
        token,
        stepUpProof: input.secretRevealProof,
        signal: input.signal,
        body,
      });
      return mapBackupAssetSearch(raw);
    },
  };
}
